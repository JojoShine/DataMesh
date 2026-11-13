package datamesh.governance;

import java.io.*;
import java.lang.reflect.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.security.KeyStore;
import java.util.concurrent.atomic.AtomicReference;
import javax.net.ssl.*;
import org.apache.flink.table.api.EnvironmentSettings;
import org.apache.flink.table.api.TableEnvironment;
import org.apache.flink.table.functions.UserDefinedFunction;
import org.apache.flink.table.api.JsonValueOnEmptyOrError;
import org.apache.flink.table.runtime.functions.SqlJsonUtils;

public final class StreamScriptFunctionTest {
    public static void main(String[] args) throws Exception {
        Class<?> type;
        try { type = Class.forName("datamesh.governance.StreamScriptFunction"); }
        catch (ClassNotFoundException missing) { throw new AssertionError("stream adapter must be available to Flink", missing); }
        Object function = type.getConstructor().newInstance();
        check(Boolean.TRUE.equals(type.getMethod("isDeterministic").invoke(function)), "memoized mapping must be deterministic");
        check(Boolean.FALSE.equals(type.getMethod("supportsConstantFolding").invoke(function)), "planner must never execute scripts");
        Method signature = type.getDeclaredMethod("signature", String.class, String.class, byte[].class);
        signature.setAccessible(true);
        byte[] body = ("{\"stream_id\":\"" + "a".repeat(32) + "\",\"generation\":2,\"node_id\":\"script\",\"row_key\":\"key\",\"payload\":{\"id\":\"9223372036854775807\",\"name\":\"\\u4e2d\\u6587\"}}").getBytes(StandardCharsets.UTF_8);
        check(signature.invoke(null, "a".repeat(64), "1700000000", body).equals("e8d60eb3c06d044a909bb1f653380926598ddbbc9ec859ad989da8d770bce51e"), "HMAC wire vector mismatch");
        Method response = type.getDeclaredMethod("readResponse", InputStream.class);
        response.setAccessible(true);
        String valid = "HTTP/1.1 200 OK\r\nContent-Length: 11\r\nContent-Type: application/json\r\n\r\n{\"id\":\"1\"}";
        // The body has ten bytes; truncation must fail even when it parses as JSON.
        expectFailure(response, valid, "truncated response");
        check(response.invoke(null, bytes(valid.replace("Length: 11", "Length: 10"))).equals("{\"id\":\"1\"}"), "response changed numeric string");
        for (String invalid : new String[]{
                "HTTP/1.1 307 Temporary Redirect\r\nContent-Length: 2\r\n\r\n{}",
                "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
                "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nContent-Length: 2\r\n\r\n{}",
                "HTTP/1.1 200 OK\r\nContent-Length: 262145\r\n\r\n",
                "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n[]",
                "HTTP/1.1 200 OK\r\nContent-Length: 7\r\n\r\n{}true "}) {
            expectFailure(response, invalid, "unsafe response accepted");
        }
        System.out.println("Stream adapter protocol tests passed");
        check(SqlJsonUtils.jsonValue("{\"optional\":null}", "lax $.optional", JsonValueOnEmptyOrError.NULL, null,
                JsonValueOnEmptyOrError.ERROR, null) == null, "explicit nullable JSON null must remain SQL null");
        check(SqlJsonUtils.jsonValue("{\"name\":\"中文\"}", "lax $.name", JsonValueOnEmptyOrError.NULL, null,
                JsonValueOnEmptyOrError.ERROR, null).equals("中文"), "nullable scalar must retain its value");
        for (String input : new String[]{"{\"id\":null}", "{}", "{\"id\":{}}", "invalid"}) {
            boolean rejected = false;
            try { SqlJsonUtils.jsonValue(input, "strict $.id", JsonValueOnEmptyOrError.ERROR, null, JsonValueOnEmptyOrError.ERROR, null); }
            catch (RuntimeException expected) { rejected = true; }
            check(rejected, "required extraction accepted null, missing, non-scalar or invalid JSON");
        }
        System.out.println("Actual Flink JSON runtime preserved nullable null and rejected invalid required values");
        if (args.length == 0) {
            TableEnvironment planner = TableEnvironment.create(EnvironmentSettings.inStreamingMode());
            planner.createTemporarySystemFunction("dm_stream_script", type.asSubclass(UserDefinedFunction.class));
            String plan = planner.explainSql("SELECT CAST(JSON_VALUE(dm_stream_script('" + "a".repeat(32)
                    + "',1,'script','row','{\"id\":\"9223372036854775807\"}'),'$.id' RETURNING STRING ERROR ON EMPTY ERROR ON ERROR) AS BIGINT)");
            check(plan.contains("dm_stream_script"), "planner evaluated the script instead of retaining the call");
            System.out.println("Flink planner retained typed script call without executing it");
            String nullPlan = planner.explainSql("SELECT CAST(JSON_VALUE('{\"optional\":null}', 'lax $.optional' RETURNING STRING NULL ON EMPTY ERROR ON ERROR) AS STRING) AS nullable_value");
            String nullOptimized = nullPlan.substring(nullPlan.indexOf("== Optimized Physical Plan =="));
            check(!nullOptimized.toUpperCase(java.util.Locale.ROOT).contains("JSON_VALUE") && nullOptimized.toLowerCase(java.util.Locale.ROOT).contains("null"),
                    "Flink SQL planner did not evaluate nullable JSON extraction to SQL null");
            String valuePlan = planner.explainSql("SELECT CAST(JSON_VALUE('{\"optional\":\"中文\"}', 'lax $.optional' RETURNING STRING NULL ON EMPTY ERROR ON ERROR) AS STRING) AS nullable_value");
            String valueOptimized = valuePlan.substring(valuePlan.indexOf("== Optimized Physical Plan =="));
            check(!valueOptimized.toUpperCase(java.util.Locale.ROOT).contains("JSON_VALUE") && valueOptimized.contains("中文"),
                    "Flink SQL planner changed the nullable scalar");
            System.out.println("Actual Flink SQL SELECT planner evaluated nullable extraction to null and preserved Unicode scalar");
        }
        if (args.length > 0) tlsRoundTrip(type, function, Path.of(args[0]), args.length > 1);
    }
    private static InputStream bytes(String value) { return new ByteArrayInputStream(value.getBytes(StandardCharsets.UTF_8)); }
    private static void expectFailure(Method method, String value, String message) throws Exception {
        try { method.invoke(null, bytes(value)); throw new AssertionError(message); }
        catch (InvocationTargetException expected) { check(expected.getCause() instanceof IOException, "wrong failure type"); }
    }
    private static void check(boolean condition, String message) { if (!condition) throw new AssertionError(message); }

    private static void tlsRoundTrip(Class<?> type, Object function, Path keyFile, boolean rejectPeer) throws Exception {
        KeyStore keys = KeyStore.getInstance("PKCS12");
        try (InputStream input = Files.newInputStream(keyFile)) { keys.load(input, "test-password".toCharArray()); }
        KeyManagerFactory managers = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
        managers.init(keys, "test-password".toCharArray());
        SSLContext context = SSLContext.getInstance("TLS"); context.init(managers.getKeyManagers(), null, null);
        AtomicReference<Throwable> serverFailure = new AtomicReference<>();
        try (SSLServerSocket server = (SSLServerSocket) context.getServerSocketFactory().createServerSocket()) {
            server.setReuseAddress(true);
            server.bind(new java.net.InetSocketAddress(18443));
            server.setSoTimeout(10000);
            Thread responder = new Thread(() -> {
                try (SSLSocket socket = (SSLSocket) server.accept()) {
                    socket.setSoTimeout(10000);
                    InputStream input = socket.getInputStream();
                    ByteArrayOutputStream header = new ByteArrayOutputStream();
                    while (!header.toString(StandardCharsets.US_ASCII).endsWith("\r\n\r\n")) {
                        int next = input.read(); if (next < 0 || header.size() > 16384) throw new IOException("bad request"); header.write(next);
                    }
                    String text = header.toString(StandardCharsets.US_ASCII);
                    check(text.startsWith("POST /internal/governance/stream-script HTTP/1.1\r\n"), "wrong endpoint");
                    String length = null, time = null, signature = null;
                    for (String line : text.split("\r\n")) {
                        if (line.startsWith("Content-Length: ")) length = line.substring(16);
                        if (line.startsWith("X-Datamesh-Script-Time: ")) time = line.substring("X-Datamesh-Script-Time: ".length());
                        if (line.startsWith("X-Datamesh-Script-Signature: ")) signature = line.substring("X-Datamesh-Script-Signature: ".length());
                    }
                    byte[] body = input.readNBytes(Integer.parseInt(length));
                    String row = new String(body, StandardCharsets.UTF_8);
                    check(row.contains("9223372036854775807") && row.contains("中文"), "numeric/Unicode input changed");
                    Method sign = type.getDeclaredMethod("signature", String.class, String.class, byte[].class); sign.setAccessible(true);
                    check(sign.invoke(null, "a".repeat(64), time, body).equals(signature), "request authentication mismatch");
                    byte[] output = "{\"id\":\"9223372036854775807\",\"name\":\"中文\",\"optional\":null}".getBytes(StandardCharsets.UTF_8);
                    socket.getOutputStream().write(("HTTP/1.1 200 OK\r\nContent-Length: " + output.length + "\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
                    socket.getOutputStream().write(output); socket.getOutputStream().flush();
                } catch (Throwable failure) { serverFailure.set(failure); }
            });
            responder.start();
            try {
                Object result = type.getMethod("eval", String.class, Integer.class, String.class, String.class, String.class)
                        .invoke(function, "a".repeat(32), 2, "script", "row", "{\"id\":\"9223372036854775807\",\"name\":\"中文\"}");
                check(!rejectPeer, "unverified TLS peer accepted");
                check(result.toString().contains("9223372036854775807") && result.toString().contains("中文"), "roundtrip changed data");
            } catch (InvocationTargetException failure) {
                if (!rejectPeer) throw failure;
                check(failure.getCause().getMessage().equals("STREAM_SCRIPT_EXECUTION_FAILED"), "unsafe error detail");
            }
            responder.join(12000);
            check(!responder.isAlive(), "TLS test did not finish");
            if (!rejectPeer && serverFailure.get() != null) throw new AssertionError("TLS server failed", serverFailure.get());
        }
        System.out.println(rejectPeer ? "Wrong TLS peer rejected" : "Authenticated TLS roundtrip passed");
    }
}
