package datamesh.source;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.*;
import com.mongodb.*;
import com.mongodb.client.*;
import com.sun.net.httpserver.*;
import org.bson.Document;
import org.bson.json.JsonMode;
import org.bson.json.JsonWriterSettings;

import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;
import java.io.*;
import java.net.*;
import java.net.http.*;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.sql.*;
import java.time.Duration;
import java.util.*;
import java.util.concurrent.*;

public final class Main {
    static final int MAX_ROWS=10000, MAX_BYTES=20*1024*1024;
    static final ObjectMapper JSON=new ObjectMapper().enable(DeserializationFeature.USE_BIG_DECIMAL_FOR_FLOATS);
    static final HttpClient HTTP=HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(5)).followRedirects(HttpClient.Redirect.NEVER).build();
    static final ScheduledExecutorService TIMEOUTS=Executors.newSingleThreadScheduledExecutor(r->{Thread t=new Thread(r,"source-read-timeout");t.setDaemon(true);return t;});
    static final Set<String> JDBC=Set.of("sqlserver","dm","kingbase","highgo","vastbase","doris");
    static final Set<String> KINDS=Set.of("sqlserver","dm","kingbase","highgo","vastbase","doris","clickhouse","mongodb","elasticsearch","redis","hbase");
    static final Map<String,List<String>> DRIVERS=Map.of(
        "sqlserver",List.of("com.microsoft.sqlserver.jdbc.SQLServerDriver"),
        "dm",List.of("dm.jdbc.driver.DmDriver"), "kingbase",List.of("com.kingbase8.Driver"),
        "highgo",List.of("com.highgo.jdbc.Driver"),
        "vastbase",List.of("cn.com.vastbase.Driver","org.postgresql.Driver"),
        "doris",List.of("com.mysql.cj.jdbc.Driver"));
    static byte[] token;
    static final class Fault extends RuntimeException {
        final String code;
        Fault(String code){super(code);this.code=code;}
    }
    record Credential(String username,String password){}
    record Input(String action,String kind,String host,int port,String database,String scheme,
                 Credential credentials,String schema,String table,int max_rows,String auth_database){}
    static Map<String,Object> field(String name,String type,boolean key,boolean nullable,Integer precision,Integer scale){
        Map<String,Object> f=new LinkedHashMap<>();
        f.put("name",name);f.put("logical_type",type);f.put("primary_key",key);f.put("nullable",nullable);
        f.put("sensitive",false);f.put("precision",precision);f.put("scale",scale);return f;
    }
    static Map<String,Object> envelopeSchema(Input in){
        List<Map<String,Object>> f=new ArrayList<>();
        f.add(field("record_key","string",true,false,null,null));
        if(in.kind.equals("redis"))f.add(field("value_type","string",false,false,null,null));
        f.add(field("document","string",false,false,null,null));
        return Map.of("schema",in.schema,"table",in.table,"fields",f,"keys",List.of("record_key"));
    }
    static final class Rows {
        final List<Map<String,Object>> items=new ArrayList<>();
        int bytes;
        void add(Map<String,Object> value) throws IOException {
            bytes+=JSON.writeValueAsBytes(value).length;
            if(items.size()>=MAX_ROWS||bytes>MAX_BYTES)throw new Fault("SOURCE_READ_LIMIT");
            items.add(value);
        }
        Map<String,Object> result(){return Map.of("records",items,"total",items.size());}
    }
    static String text(JsonNode n,String name){return n.path(name).asText();}
    static String segment(String value){return URLEncoder.encode(value,StandardCharsets.UTF_8).replace("+","%20");}
    static String address(Input in){return (in.host.contains(":")?"["+in.host+"]":in.host)+":"+in.port;}
    static String driver(String kind){
        for(String name:DRIVERS.getOrDefault(kind,List.of())){
            try{Class<?> type=Class.forName(name);if(kind.equals("vastbase")&&name.equals("org.postgresql.Driver")&&!type.getProtectionDomain().getCodeSource().getLocation().toString().toLowerCase(Locale.ROOT).contains("vastbase"))continue;return name;}catch(ClassNotFoundException ignored){/* Driver is optional, never substituted. */}catch(LinkageError failure){throw new Fault("SOURCE_DRIVER_INVALID");}
        }
        return "";
    }
    static Map<String,Object> capabilities(){
        List<Map<String,Object>> items=new ArrayList<>();
        for(String k:new TreeSet<>(KINDS)){
            String status;try{status=!JDBC.contains(k)||!driver(k).isEmpty()?"ready":"driver_missing";}catch(Fault e){status="driver_invalid";}
            items.add(Map.of("kind",k,"ready",status.equals("ready"),"status",status));
        }
        return Map.of("items",items);
    }
    static void validate(Input in){
        if(in==null||!KINDS.contains(in.kind)||in.credentials==null||in.host==null||in.host.isBlank()||
            in.host.length()>253||in.host.matches(".*[\\s/@?#\\\\].*")||in.port<1||in.port>65535||
            in.database==null||in.database.isBlank()||in.database.length()>128||
            !Set.of("probe","objects","schema","read").contains(in.action))throw new Fault("INVALID_REQUEST");
        if(in.credentials.username!=null&&in.credentials.password!=null&&((in.kind.equals("mongodb")&&(in.credentials.username.isEmpty()!=in.credentials.password.isEmpty()))||(in.kind.equals("redis")&&!in.credentials.username.isEmpty()&&in.credentials.password.isEmpty())))throw new Fault("INVALID_REQUEST");
        if(in.max_rows<0||in.max_rows>MAX_ROWS)throw new Fault("INVALID_REQUEST");
        if(in.scheme!=null&&!in.scheme.isEmpty()&&!Set.of("http","https").contains(in.scheme))throw new Fault("INVALID_REQUEST");
        if(in.credentials.username==null||in.credentials.password==null||in.credentials.username.length()>128||in.credentials.password.length()>4096)
            throw new Fault("INVALID_REQUEST");
        if(Set.of("schema","read").contains(in.action)&&(in.schema==null||in.table==null||in.table.isBlank()||
            in.schema.length()>128||in.table.length()>128))throw new Fault("INVALID_REQUEST");
    }
    static JsonNode http(Input in,String path) throws Exception {
        String scheme=in.scheme==null||in.scheme.isEmpty()?"https":in.scheme;
        URI uri=URI.create(scheme+"://"+address(in)+path);
        HttpRequest.Builder b=HttpRequest.newBuilder(uri).timeout(Duration.ofSeconds(8)).header("Accept","application/json");
        if(!in.credentials.username.isEmpty()||!in.credentials.password.isEmpty()){
            String basic=Base64.getEncoder().encodeToString((in.credentials.username+":"+in.credentials.password).getBytes(StandardCharsets.UTF_8));
            b.header("Authorization","Basic "+basic);
        }
        HttpResponse<InputStream> r=HTTP.send(b.GET().build(),HttpResponse.BodyHandlers.ofInputStream());
        InputStream stream=r.body();
        ScheduledFuture<?> deadline=TIMEOUTS.schedule(()->{try{stream.close();}catch(IOException ignored){/* Closing only cancels this bounded response. */}},8,TimeUnit.SECONDS);
        try(InputStream body=stream){
            if(r.statusCode()==401||r.statusCode()==403)throw new Fault("SOURCE_AUTH_FAILED");
            if(r.statusCode()<200||r.statusCode()>=300)throw new Fault("SOURCE_QUERY_FAILED");
            byte[] raw=body.readNBytes(MAX_BYTES+1);
            if(raw.length>MAX_BYTES)throw new Fault("SOURCE_READ_LIMIT");
            return JSON.readTree(raw);
        }finally{deadline.cancel(false);}
    }
    static Map<String,Object> object(String schema,String table,String kind){return Map.of("schema",schema,"name",table,"kind",kind);}
    static Object jdbc(Input in) throws Exception {
        String loadedDriver=driver(in.kind);if(loadedDriver.isEmpty())throw new Fault("SOURCE_DRIVER_MISSING");
        Properties props=new Properties();props.setProperty("user",in.credentials.username);props.setProperty("password",in.credentials.password);
        props.setProperty("connectTimeout",Set.of("kingbase","highgo","vastbase").contains(in.kind)?"5":"5000");props.setProperty("socketTimeout",Set.of("kingbase","highgo","vastbase").contains(in.kind)?"8":"8000");
        String prefix=Map.of("dm","dm","kingbase","kingbase8","highgo","highgo","vastbase","vastbase","doris","mysql","sqlserver","sqlserver").get(in.kind);
        if(in.kind.equals("vastbase")&&loadedDriver.equals("org.postgresql.Driver"))prefix="postgresql";
        String url="jdbc:"+prefix+"://"+address(in);
        if(in.kind.equals("sqlserver")){
            props.setProperty("databaseName",in.database);props.setProperty("encrypt","true");props.setProperty("trustServerCertificate","false");
            props.setProperty("loginTimeout","5");props.setProperty("socketTimeout","8000");
        }else if(in.kind.equals("dm"))props.setProperty("schema",in.database);
        else url+="/"+segment(in.database);
        DriverManager.setLoginTimeout(5);
        try(Connection db=DriverManager.getConnection(url,props)){
            if(in.action.equals("probe")){if(!db.isValid(5))throw new Fault("SOURCE_UNAVAILABLE");return Map.of("passed",true);}
            DatabaseMetaData meta=db.getMetaData();String catalog=Set.of("doris","sqlserver").contains(in.kind)?in.database:null;
            if(in.action.equals("objects")){
                List<Map<String,Object>> items=new ArrayList<>();
                try(ResultSet rs=meta.getTables(catalog,in.kind.equals("dm")?in.database:null,"%",new String[]{"TABLE","VIEW"})){
                    while(rs.next()){
                        String schema=rs.getString("TABLE_SCHEM");if(schema==null||schema.isEmpty())schema=rs.getString("TABLE_CAT");
                        if(schema==null)schema=in.database;
                        if(in.kind.equals("dm")&&!schema.equals(in.database))continue;
                        if(Set.of("information_schema","pg_catalog","sys","SYS","SYSTEM","XDB","MDSYS","CTXSYS").contains(schema))continue;
                        if(items.size()>=1000)throw new Fault("SOURCE_OBJECT_LIMIT");
                        items.add(object(schema,rs.getString("TABLE_NAME"),rs.getString("TABLE_TYPE")));
                    }
                }
                return Map.of("items",items);
            }
            if(in.kind.equals("dm")&&!in.schema.equals(in.database))throw new Fault("SOURCE_SCOPE_INVALID");
            if(in.kind.equals("doris")&&!in.schema.equals(in.database))throw new Fault("SOURCE_SCOPE_INVALID");
            String schema=Set.of("doris").contains(in.kind)?null:in.schema;
            List<String> keys=new ArrayList<>();TreeMap<Integer,String> ordered=new TreeMap<>();
            try(ResultSet rs=meta.getPrimaryKeys(catalog,schema,in.table)){
                while(rs.next())ordered.put(rs.getInt("KEY_SEQ"),rs.getString("COLUMN_NAME"));
            }
            keys.addAll(ordered.values());
            List<Map<String,Object>> fields=new ArrayList<>();
            String pattern=in.table.replace(meta.getSearchStringEscape(),meta.getSearchStringEscape()+meta.getSearchStringEscape())
                .replace("_",meta.getSearchStringEscape()+"_").replace("%",meta.getSearchStringEscape()+"%");
            String schemaPattern=schema==null?null:schema.replace(meta.getSearchStringEscape(),meta.getSearchStringEscape()+meta.getSearchStringEscape()).replace("_",meta.getSearchStringEscape()+"_").replace("%",meta.getSearchStringEscape()+"%");
            try(ResultSet rs=meta.getColumns(catalog,schemaPattern,pattern,null)){
                while(rs.next()){
                    if(!in.table.equals(rs.getString("TABLE_NAME"))||(schema!=null&&!schema.equals(rs.getString("TABLE_SCHEM"))))continue;
                    String n=rs.getString("COLUMN_NAME");int type=rs.getInt("DATA_TYPE");String logical=logical(type);
                    Integer precision=logical.equals("decimal")?rs.getInt("COLUMN_SIZE"):null,scale=logical.equals("decimal")?rs.getInt("DECIMAL_DIGITS"):null;
                    fields.add(field(n,logical,keys.contains(n),rs.getInt("NULLABLE")!=DatabaseMetaData.columnNoNulls,precision,scale));
                    if(fields.size()>100)throw new Fault("TABLE_FIELD_LIMIT");
                }
            }
            if(fields.isEmpty())throw new Fault("TABLE_NOT_FOUND");
            if(in.action.equals("schema"))return Map.of("schema",in.schema,"table",in.table,"fields",fields,"keys",keys);
            if(fields.stream().anyMatch(f->f.get("logical_type").equals("unsupported")))throw new Fault("SOURCE_TYPE_UNSUPPORTED");
            String quote=meta.getIdentifierQuoteString().strip();if(quote.isEmpty())throw new Fault("SOURCE_QUERY_FAILED");
            String quoted=quote+in.schema.replace(quote,quote+quote)+quote+"."+quote+in.table.replace(quote,quote+quote)+quote;
            List<String> columns=fields.stream().map(f->quote+f.get("name").toString().replace(quote,quote+quote)+quote).toList();
            Rows rows=new Rows();
            try(Statement statement=db.createStatement()){
                statement.setQueryTimeout(8);statement.setMaxRows(MAX_ROWS+1);statement.setFetchSize(250);
                try(ResultSet rs=statement.executeQuery("SELECT "+String.join(",",columns)+" FROM "+quoted)){
                    while(rs.next()){
                        Map<String,Object> record=new LinkedHashMap<>();
                        for(Map<String,Object> f:fields){
                            String name=f.get("name").toString(),type=f.get("logical_type").toString();Object value;
                            if(type.equals("boolean")){value=rs.getBoolean(name);if(rs.wasNull())value=null;}
                            else if(type.equals("datetime")){Timestamp timestamp=rs.getTimestamp(name);value=timestamp==null?null:timestamp.toInstant().toString();}
                            else if(type.equals("date")){java.sql.Date date=rs.getDate(name);value=date==null?null:date.toLocalDate().toString();}
                            else if(type.equals("integer"))value=rs.getString(name);
                            else if(type.equals("decimal")){java.math.BigDecimal decimal=rs.getBigDecimal(name);value=decimal==null?null:decimal.toPlainString();}
                            else {
                                Reader reader=rs.getCharacterStream(name);
                                if(reader==null)value=null;else try(reader){StringBuilder b=new StringBuilder();char[] block=new char[8192];int size;while((size=reader.read(block))!=-1){if(b.length()+size>MAX_BYTES)throw new Fault("SOURCE_READ_LIMIT");b.append(block,0,size);}value=b.toString();}
                            }
                            record.put(name,value);
                        }
                        rows.add(record);
                    }
                }
            }
            return rows.result();
        }
    }
    static String logical(int type){
        return switch(type){
            case Types.TINYINT,Types.SMALLINT,Types.INTEGER,Types.BIGINT -> "integer";
            case Types.NUMERIC,Types.DECIMAL,Types.FLOAT,Types.DOUBLE,Types.REAL -> "decimal";
            case Types.BOOLEAN,Types.BIT -> "boolean";
            case Types.DATE -> "date";
            case Types.TIMESTAMP,Types.TIMESTAMP_WITH_TIMEZONE -> "datetime";
            case Types.CHAR,Types.VARCHAR,Types.LONGVARCHAR,Types.NCHAR,Types.NVARCHAR,Types.LONGNVARCHAR,Types.CLOB,Types.NCLOB -> "string";
            default -> "unsupported";
        };
    }
    static Object mongo(Input in) throws Exception {
        MongoClientSettings.Builder settings=MongoClientSettings.builder()
            .applyToClusterSettings(b->b.hosts(List.of(new ServerAddress(in.host,in.port))).mode(com.mongodb.connection.ClusterConnectionMode.SINGLE).serverSelectionTimeout(5,TimeUnit.SECONDS))
            .applyToSocketSettings(b->b.connectTimeout(5,TimeUnit.SECONDS).readTimeout(8,TimeUnit.SECONDS))
            .retryReads(false).retryWrites(false);
        if(!in.credentials.username.isEmpty())settings.credential(MongoCredential.createCredential(in.credentials.username,in.auth_database==null||in.auth_database.isEmpty()?in.database:in.auth_database,in.credentials.password.toCharArray()));
        if("https".equals(in.scheme))settings.applyToSslSettings(b->b.enabled(true));
        try(MongoClient client=MongoClients.create(settings.build())){
            MongoDatabase db=client.getDatabase(in.database);
            if(in.action.equals("probe")){db.runCommand(new Document("ping",1));return Map.of("passed",true);}
            List<String> names=db.listCollectionNames().maxTime(8,TimeUnit.SECONDS).into(new ArrayList<>());
            if(names.size()>1000)throw new Fault("SOURCE_OBJECT_LIMIT");
            if(in.action.equals("objects"))return Map.of("items",names.stream().map(n->object(in.database,n,"COLLECTION")).toList());
            if(!in.database.equals(in.schema)||!names.contains(in.table))throw new Fault("TABLE_NOT_FOUND");
            if(in.action.equals("schema"))return envelopeSchema(in);
            Rows rows=new Rows();JsonWriterSettings json=JsonWriterSettings.builder().outputMode(JsonMode.EXTENDED).build();
            try(MongoCursor<Document> cursor=db.getCollection(in.table).find().limit(MAX_ROWS+1).maxTime(8,TimeUnit.SECONDS).iterator()){
                while(cursor.hasNext()){
                    Document document=cursor.next();String key=new Document("_id",document.get("_id")).toJson(json);
                    rows.add(Map.of("record_key",key,"document",document.toJson(json)));
                }
            }
            return rows.result();
        }
    }
    static JsonNode clickhouseQuery(Input in,String sql,Map<String,String> params) throws Exception{
        StringBuilder path=new StringBuilder("/?database=").append(segment(in.database)).append("&query=").append(segment(sql+" FORMAT JSON"));
        for(var p:params.entrySet())path.append("&param_").append(segment(p.getKey())).append("=").append(segment(p.getValue()));
        return http(in,path.toString());
    }
    static Object clickhouse(Input in) throws Exception{
        if(in.action.equals("probe")){clickhouseQuery(in,"SELECT 1",Map.of());return Map.of("passed",true);}
        JsonNode objects=clickhouseQuery(in,"SELECT name,engine FROM system.tables WHERE database={db:String} ORDER BY name LIMIT 1001",Map.of("db",in.database)).path("data");
        if(objects.size()>1000)throw new Fault("SOURCE_OBJECT_LIMIT");
        if(in.action.equals("objects")){
            List<Map<String,Object>> items=new ArrayList<>();for(JsonNode n:objects)items.add(object(in.database,text(n,"name"),text(n,"engine")));
            return Map.of("items",items);
        }
        if(!in.database.equals(in.schema))throw new Fault("SOURCE_SCOPE_INVALID");
        boolean exists=false;for(JsonNode n:objects)if(text(n,"name").equals(in.table))exists=true;
        if(!exists)throw new Fault("TABLE_NOT_FOUND");
        JsonNode columns=clickhouseQuery(in,"SELECT name,type FROM system.columns WHERE database={db:String} AND table={tbl:String} ORDER BY position LIMIT 101",Map.of("db",in.database,"tbl",in.table)).path("data");
        if(columns.isEmpty()||columns.size()>100)throw new Fault("TABLE_FIELD_LIMIT");
        List<Map<String,Object>> fields=new ArrayList<>();
        for(JsonNode n:columns){String t=text(n,"type");String basic=t;while((basic.startsWith("Nullable(")||basic.startsWith("LowCardinality("))&&basic.endsWith(")"))basic=basic.substring(basic.indexOf('(')+1,basic.length()-1);
            String logical=basic.matches("U?Int.*")?"integer":basic.startsWith("Decimal")||basic.startsWith("Float")?"decimal":basic.startsWith("DateTime")?"datetime":basic.startsWith("Date")?"date":basic.equals("Bool")?"boolean":basic.startsWith("String")||basic.startsWith("FixedString")||basic.equals("UUID")?"string":"unsupported";
            Integer precision=null,scale=null;
            var decimal=java.util.regex.Pattern.compile("^Decimal\\((\\d+),\\s*(\\d+)\\)$").matcher(basic);
            var sized=java.util.regex.Pattern.compile("^Decimal(32|64|128|256)\\((\\d+)\\)$").matcher(basic);
            if(decimal.matches()){precision=Integer.valueOf(decimal.group(1));scale=Integer.valueOf(decimal.group(2));}
            else if(sized.matches()){precision=Map.of("32",9,"64",18,"128",38,"256",76).get(sized.group(1));scale=Integer.valueOf(sized.group(2));}
            fields.add(field(text(n,"name"),logical,false,t.contains("Nullable("),precision,scale));
        }
        if(in.action.equals("schema"))return Map.of("schema",in.schema,"table",in.table,"fields",fields,"keys",List.of());
        if(fields.stream().anyMatch(f->f.get("logical_type").equals("unsupported")))throw new Fault("SOURCE_TYPE_UNSUPPORTED");
        String quote="\"";
        String sql="SELECT "+String.join(",",fields.stream().map(f->quote+f.get("name").toString().replace("\"","\"\"")+quote).toList())+" FROM "+quote+in.schema.replace("\"","\"\"")+quote+"."+quote+in.table.replace("\"","\"\"")+quote+" LIMIT 10001 SETTINGS output_format_json_quote_64bit_integers=1,date_time_output_format='iso'";
        JsonNode data=clickhouseQuery(in,sql,Map.of()).path("data");Rows rows=new Rows();
        for(JsonNode n:data)rows.add(JSON.convertValue(n,new TypeReference<Map<String,Object>>(){}));
        return rows.result();
    }
    static Object elasticsearch(Input in) throws Exception{
        String index="/"+segment(in.database);
        JsonNode mapping=http(in,index+"/_mapping");if(!mapping.isObject()||mapping.isEmpty())throw new Fault("SOURCE_QUERY_FAILED");
        if(in.action.equals("probe"))return Map.of("passed",true);
        if(in.action.equals("objects"))return Map.of("items",List.of(object(in.database,in.database,"INDEX")));
        if(!in.schema.equals(in.database)||!in.table.equals(in.database))throw new Fault("SOURCE_SCOPE_INVALID");
        if(in.action.equals("schema"))return envelopeSchema(in);
        JsonNode result=http(in,index+"/_search?size=10000&track_total_hits=true");
        JsonNode count=result.path("hits").path("total");long total=count.isNumber()?count.asLong():count.path("value").asLong(-1);
        if(total<0||total>MAX_ROWS||(!count.isNumber()&&!text(count,"relation").equals("eq")))throw new Fault("SOURCE_READ_LIMIT");
        if(result.path("timed_out").asBoolean(false)||result.path("_shards").path("failed").asInt(0)>0)throw new Fault("SOURCE_CHANGED");
        JsonNode hits=result.path("hits").path("hits");
        if(hits.size()!=total)throw new Fault("SOURCE_CHANGED");
        Rows rows=new Rows();for(JsonNode hit:hits){
            if(!hit.path("_source").isObject()||text(hit,"_id").isEmpty())throw new Fault("SOURCE_TYPE_UNSUPPORTED");
            rows.add(Map.of("record_key",text(hit,"_index")+"/"+text(hit,"_id"),"document",JSON.writeValueAsString(hit.path("_source"))));
        }
        return rows.result();
    }
    static Object hbase(Input in) throws Exception{
        JsonNode tables=http(in,"/");if(!tables.path("table").isArray())throw new Fault("SOURCE_QUERY_FAILED");
        List<String> names=new ArrayList<>();
        for(JsonNode n:tables.path("table")){
            String name=text(n,"name"),namespace=name.contains(":")?name.substring(0,name.indexOf(':')):"default";
            if(namespace.equals(in.database))names.add(name);
        }
        if(in.action.equals("probe"))return Map.of("passed",true);
        if(names.size()>1000)throw new Fault("SOURCE_OBJECT_LIMIT");
        if(in.action.equals("objects"))return Map.of("items",names.stream().map(n->object(in.database,n,"HBASE_TABLE")).toList());
        if(!in.schema.equals(in.database)||!names.contains(in.table))throw new Fault("TABLE_NOT_FOUND");
        if(in.action.equals("schema"))return envelopeSchema(in);
        JsonNode data=http(in,"/"+segment(in.table)+"/*?limit=10001");
        if(!data.path("Row").isArray())throw new Fault("SOURCE_QUERY_FAILED");
        Rows rows=new Rows();for(JsonNode row:data.path("Row"))rows.add(Map.of("record_key",text(row,"key"),"document",JSON.writeValueAsString(row)));
        return rows.result();
    }
    static final class Redis implements AutoCloseable {
        final Socket socket;final InputStream input;final OutputStream output;int received;final long deadline=System.nanoTime()+TimeUnit.SECONDS.toNanos(10);
        Redis(Input in)throws Exception{
            socket="https".equals(in.scheme)?SSLSocketFactory.getDefault().createSocket():new Socket();
            socket.connect(new InetSocketAddress(in.host,in.port),5000);socket.setSoTimeout(8000);
            if(socket instanceof SSLSocket ssl){var p=ssl.getSSLParameters();p.setEndpointIdentificationAlgorithm("HTTPS");ssl.setSSLParameters(p);ssl.startHandshake();}
            input=new BufferedInputStream(socket.getInputStream());output=new BufferedOutputStream(socket.getOutputStream());
            try{
                if(!in.credentials.password.isEmpty()){
                    if(in.credentials.username.isEmpty())command("AUTH",in.credentials.password);else command("AUTH",in.credentials.username,in.credentials.password);
                }
                int database=Integer.parseInt(in.database);if(database<0||database>15)throw new Fault("SOURCE_SCOPE_INVALID");
                command("SELECT",Integer.toString(database));
            }catch(Exception e){socket.close();throw e;}
        }
        Object command(String...args)throws Exception{return commandBytes(Arrays.stream(args).map(s->s.getBytes(StandardCharsets.UTF_8)).toList());}
        Object commandBytes(List<byte[]>args)throws Exception{
            if(System.nanoTime()>deadline)throw new Fault("SOURCE_UNAVAILABLE");
            output.write(("*"+args.size()+"\r\n").getBytes(StandardCharsets.US_ASCII));
            for(byte[] a:args){output.write(("$"+a.length+"\r\n").getBytes(StandardCharsets.US_ASCII));output.write(a);output.write("\r\n".getBytes(StandardCharsets.US_ASCII));}
            output.flush();return read(0);
        }
        int next()throws Exception{if(++received>MAX_BYTES)throw new Fault("SOURCE_READ_LIMIT");int n=input.read();if(n<0)throw new EOFException();return n;}
        String line()throws Exception{ByteArrayOutputStream b=new ByteArrayOutputStream();int n;while((n=next())!='\r'){if(b.size()>4096)throw new Fault("SOURCE_READ_LIMIT");b.write(n);}if(next()!='\n')throw new Fault("SOURCE_QUERY_FAILED");return b.toString(StandardCharsets.US_ASCII);}
        Object read(int depth)throws Exception{
            if(depth>16)throw new Fault("SOURCE_READ_LIMIT");int kind=next();String n=line();
            if(kind=='+')return n;if(kind=='-')throw new Fault(n.startsWith("NOAUTH")||n.startsWith("WRONGPASS")?"SOURCE_AUTH_FAILED":"SOURCE_QUERY_FAILED");
            if(kind==':')return n;
            int size=Integer.parseInt(n);if(size==-1)return null;if(size<0||size>MAX_BYTES)throw new Fault("SOURCE_READ_LIMIT");
            if(kind=='$'){if(received+size>MAX_BYTES)throw new Fault("SOURCE_READ_LIMIT");byte[] b=input.readNBytes(size);received+=b.length;if(b.length!=size||next()!='\r'||next()!='\n')throw new EOFException();return b;}
            if(kind=='*'){if(size>100000)throw new Fault("SOURCE_READ_LIMIT");List<Object> out=new ArrayList<>();for(int i=0;i<size;i++)out.add(read(depth+1));return out;}
            throw new Fault("SOURCE_QUERY_FAILED");
        }
        public void close()throws IOException{socket.close();}
    }
    static String redisText(Object value){return value instanceof byte[] b?new String(b,StandardCharsets.UTF_8):Objects.toString(value,"");}
    static Object redisJson(Object value){
        if(value instanceof byte[] b)return Map.of("base64",Base64.getEncoder().encodeToString(b));
        if(value instanceof List<?> list)return list.stream().map(Main::redisJson).toList();
        return value;
    }
    static Object redis(Input in)throws Exception{
        try(Redis db=new Redis(in)){
            db.command("PING");if(in.action.equals("probe"))return Map.of("passed",true);
            if(in.action.equals("objects"))return Map.of("items",List.of(object(in.database,"keys","KEYSPACE")));
            if(!in.schema.equals(in.database)||!in.table.equals("keys"))throw new Fault("SOURCE_SCOPE_INVALID");
            if(in.action.equals("schema"))return envelopeSchema(in);
            TreeMap<String,byte[]> keys=new TreeMap<>();String cursor="0";int scans=0;
            do{
                Object scan=db.command("SCAN",cursor,"COUNT","200");
                if(!(scan instanceof List<?> parts)||parts.size()!=2||!(parts.get(1)instanceof List<?> list))throw new Fault("SOURCE_QUERY_FAILED");
                cursor=redisText(parts.get(0));for(Object value:list){if(!(value instanceof byte[] key))throw new Fault("SOURCE_QUERY_FAILED");keys.put(Base64.getEncoder().encodeToString(key),key);}
                if(keys.size()>MAX_ROWS||++scans>10000)throw new Fault("SOURCE_READ_LIMIT");
            }while(!cursor.equals("0"));
            Rows rows=new Rows();
            for(var entry:keys.entrySet()){
                byte[] key=entry.getValue();String type=redisText(db.commandBytes(List.of("TYPE".getBytes(StandardCharsets.US_ASCII),key)));
                String[] cmd=switch(type){case "string"->new String[]{"GET"};case "hash"->new String[]{"HGETALL"};case "list"->new String[]{"LRANGE","0","-1"};case "set"->new String[]{"SMEMBERS"};case "zset"->new String[]{"ZRANGE","0","-1","WITHSCORES"};case "stream"->new String[]{"XRANGE","-","+","COUNT","10001"};default->throw new Fault(type.equals("none")?"SOURCE_CHANGED":"SOURCE_TYPE_UNSUPPORTED");};
                List<byte[]> args=new ArrayList<>();args.add(cmd[0].getBytes(StandardCharsets.US_ASCII));args.add(key);for(int i=1;i<cmd.length;i++)args.add(cmd[i].getBytes(StandardCharsets.US_ASCII));
                Object value=db.commandBytes(args);if(value==null)throw new Fault("SOURCE_CHANGED");
                if(type.equals("stream")&&value instanceof List<?> entries&&entries.size()>MAX_ROWS)throw new Fault("SOURCE_READ_LIMIT");
                rows.add(Map.of("record_key",entry.getKey(),"value_type",type,"document",JSON.writeValueAsString(redisJson(value))));
            }
            return rows.result();
        }
    }
    static Object run(Input in)throws Exception{
        validate(in);
        if(JDBC.contains(in.kind))return jdbc(in);
        return switch(in.kind){
            case "mongodb"->mongo(in);case "elasticsearch"->elasticsearch(in);case "redis"->redis(in);
            case "hbase"->hbase(in);case "clickhouse"->clickhouse(in);default->throw new Fault("INVALID_REQUEST");
        };
    }
    static void response(HttpExchange exchange,int status,Object value)throws IOException{
        byte[] data=JSON.writeValueAsBytes(value);
        if(data.length>MAX_BYTES){status=422;data=JSON.writeValueAsBytes(Map.of("code","SOURCE_READ_LIMIT"));}
        exchange.getResponseHeaders().set("Content-Type","application/json; charset=utf-8");
        exchange.sendResponseHeaders(status,data.length);try(OutputStream out=exchange.getResponseBody()){out.write(data);}
    }
    public static void main(String[]args)throws Exception{
        String secret=System.getenv("SOURCE_READER_TOKEN");if(secret==null||secret.length()<32)throw new IllegalStateException("SOURCE_READER_TOKEN must be configured");
        token=("Bearer "+secret).getBytes(StandardCharsets.UTF_8);
        if(args.length==1&&args[0].equals("--health")){
            HttpRequest request=HttpRequest.newBuilder(URI.create("http://127.0.0.1:8093/health/ready")).timeout(Duration.ofSeconds(3)).header("Authorization","Bearer "+secret).GET().build();
            if(HTTP.send(request,HttpResponse.BodyHandlers.discarding()).statusCode()!=200)System.exit(1);
            return;
        }
        HttpServer server=HttpServer.create(new InetSocketAddress("0.0.0.0",8093),16);
        server.setExecutor(new ThreadPoolExecutor(2,2,0,TimeUnit.SECONDS,new ArrayBlockingQueue<>(8)));
        server.createContext("/",exchange->{
            try{
                String auth=exchange.getRequestHeaders().getFirst("Authorization");
                if(auth==null||!MessageDigest.isEqual(token,auth.getBytes(StandardCharsets.UTF_8))){response(exchange,401,Map.of("code","READER_AUTH_REQUIRED"));return;}
                String path=exchange.getRequestURI().getPath();
                if(exchange.getRequestMethod().equals("GET")&&path.equals("/health/ready")){response(exchange,200,Map.of("status","ready"));return;}
                if(exchange.getRequestMethod().equals("GET")&&path.equals("/v1/capabilities")){response(exchange,200,capabilities());return;}
                if(!exchange.getRequestMethod().equals("POST")||!path.equals("/v1/source")){response(exchange,404,Map.of("code","NOT_FOUND"));return;}
                byte[] body=exchange.getRequestBody().readNBytes(1024*1024+1);if(body.length>1024*1024)throw new Fault("INVALID_REQUEST");
                Input input=JSON.readValue(body,Input.class);response(exchange,200,run(input));
            }catch(Fault e){response(exchange,422,Map.of("code",e.code));}
            catch(Exception e){response(exchange,502,Map.of("code","SOURCE_UNAVAILABLE"));}
            finally{exchange.close();}
        });
        server.start();
    }
}
