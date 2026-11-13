package datamesh.governance;

import org.apache.flink.table.functions.ScalarFunction;
import org.apache.flink.shaded.jackson2.com.fasterxml.jackson.databind.DeserializationFeature;
import org.apache.flink.shaded.jackson2.com.fasterxml.jackson.databind.JsonNode;
import org.apache.flink.shaded.jackson2.com.fasterxml.jackson.databind.ObjectMapper;
import org.apache.flink.shaded.jackson2.com.fasterxml.jackson.databind.node.ObjectNode;

import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import javax.net.ssl.*;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.security.*;
import java.security.cert.CertificateFactory;
import java.time.Instant;
import java.util.*;

/** Trusted transport only: all user code executes in the networkless sandbox. */
public final class StreamScriptFunction extends ScalarFunction {
    private static final long serialVersionUID = 1L;
    private static final int MAX_ROW = 256 * 1024;
    private static final int MAX_REQUEST = MAX_ROW + 4096;
    private static final String PATH = "/internal/governance/stream-script";
    private static final ObjectMapper JSON = new ObjectMapper().enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS);

    @Override public boolean isDeterministic() { return true; }
    // Gateway planning must not cause network calls or execute a script.
    @Override public boolean supportsConstantFolding() { return false; }

    public String eval(String streamID, Integer generation, String nodeID, String rowKey, String payload) {
        try {
            if (streamID == null || !streamID.matches("[a-f0-9]{32}") || generation == null || generation < 1
                    || nodeID == null || !nodeID.matches("[A-Za-z_][A-Za-z0-9_]{0,62}") || rowKey == null
                    || rowKey.isEmpty() || rowKey.length() > 128 || payload == null
                    || payload.getBytes(StandardCharsets.UTF_8).length > MAX_ROW) throw new IOException("invalid input");
            JsonNode row = JSON.readTree(payload);
            if (row == null || !row.isObject()) throw new IOException("invalid input");
            ObjectNode request = JSON.createObjectNode();
            request.put("stream_id", streamID); request.put("generation", generation);
            request.put("node_id", nodeID); request.put("row_key", rowKey); request.set("payload", row);
            byte[] body = JSON.writeValueAsBytes(request);
            if (body.length > MAX_REQUEST) throw new IOException("request exceeds limit");
            URI origin = URI.create(required("GOVERNANCE_STREAM_SCRIPT_ORIGIN"));
            if (!"https".equals(origin.getScheme()) || origin.getHost() == null || origin.getUserInfo() != null
                    || origin.getRawQuery() != null || origin.getRawFragment() != null
                    || !(origin.getRawPath().isEmpty() || origin.getRawPath().equals("/"))) throw new IOException("invalid bridge origin");
            String peer = required("GOVERNANCE_STREAM_SCRIPT_TLS_NAME");
            if (!peer.matches("[A-Za-z0-9.-]{1,253}")) throw new IOException("invalid TLS peer");
            String token = Files.readString(Path.of(required("GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE")), StandardCharsets.US_ASCII);
            if (!token.matches("[a-f0-9]{64}")) throw new IOException("invalid bridge credential");
            SSLContext tls = trust(Path.of(required("GOVERNANCE_STREAM_SCRIPT_CA_FILE")));
            String time = Long.toString(Instant.now().getEpochSecond());
            int port = origin.getPort() == -1 ? 443 : origin.getPort();
            try (Socket tcp = new Socket()) {
                tcp.connect(new InetSocketAddress(origin.getHost(), port), 10000);
                // The physical service address differs from the certificate's logical identity.
                // Layer TLS using the verified peer name; hostname verification stays enabled.
                try (SSLSocket socket = (SSLSocket) tls.getSocketFactory().createSocket(tcp, peer, port, true)) {
                    socket.setSoTimeout(60000);
                    SSLParameters parameters = socket.getSSLParameters();
                    parameters.setEndpointIdentificationAlgorithm("HTTPS");
                    socket.setSSLParameters(parameters);
                    socket.startHandshake();
                    OutputStream output = socket.getOutputStream();
                    String headers = "POST " + PATH + " HTTP/1.1\r\nHost: " + origin.getRawAuthority()
                            + "\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " + body.length
                            + "\r\nX-Datamesh-Script-Time: " + time + "\r\nX-Datamesh-Script-Signature: "
                            + signature(token, time, body) + "\r\n\r\n";
                    output.write(headers.getBytes(StandardCharsets.US_ASCII)); output.write(body); output.flush();
                    return readResponse(socket.getInputStream());
                }
            }
        } catch (IOException | GeneralSecurityException | IllegalArgumentException failure) {
            // Never include payloads, credentials, source text or server error bodies in Flink logs.
            throw new IllegalStateException("STREAM_SCRIPT_EXECUTION_FAILED");
        }
    }

    private static String required(String name) throws IOException {
        String value = System.getenv(name);
        if (value == null || value.isEmpty()) throw new IOException("bridge configuration unavailable");
        return value;
    }

    private static SSLContext trust(Path certificate) throws IOException, GeneralSecurityException {
        KeyStore store = KeyStore.getInstance(KeyStore.getDefaultType());
        store.load(null, null);
        try (InputStream input = Files.newInputStream(certificate)) {
            store.setCertificateEntry("bridge", CertificateFactory.getInstance("X.509").generateCertificate(input));
        }
        TrustManagerFactory managers = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
        managers.init(store);
        SSLContext context = SSLContext.getInstance("TLS");
        context.init(null, managers.getTrustManagers(), null);
        return context;
    }

    static String signature(String token, String time, byte[] body) throws GeneralSecurityException {
        String hash = HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(body));
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec(token.getBytes(StandardCharsets.US_ASCII), "HmacSHA256"));
        return HexFormat.of().formatHex(mac.doFinal((time + "\nPOST\n" + PATH + "\n" + hash).getBytes(StandardCharsets.US_ASCII)));
    }

    static String readResponse(InputStream input) throws IOException {
        ByteArrayOutputStream headers = new ByteArrayOutputStream();
        int previous = 0, matched = 0;
        while (matched < 4) {
            int next = input.read();
            if (next < 0 || headers.size() >= 16384) throw new IOException("invalid HTTP headers");
            headers.write(next);
            // Match CRLFCRLF without accepting bare line endings.
            matched = switch (matched) {
                case 0 -> next == '\r' ? 1 : 0;
                case 1 -> next == '\n' ? 2 : (next == '\r' ? 1 : 0);
                case 2 -> next == '\r' ? 3 : 0;
                default -> next == '\n' ? 4 : (next == '\r' ? 1 : 0);
            };
            if (next == '\n' && previous != '\r') throw new IOException("invalid HTTP line ending");
            previous = next;
        }
        String[] lines = headers.toString(StandardCharsets.US_ASCII).split("\r\n");
        if (!lines[0].matches("HTTP/1\\.[01] 200(?: .*)?")) throw new IOException("bridge rejected execution");
        int length = -1;
        for (int i = 1; i < lines.length; i++) {
            String line = lines[i];
            int colon = line.indexOf(':');
            if (colon < 1 || Character.isWhitespace(line.charAt(0))) throw new IOException("invalid HTTP header");
            String name = line.substring(0, colon).toLowerCase(Locale.ROOT);
            if (name.equals("transfer-encoding")) throw new IOException("chunked response forbidden");
            if (name.equals("content-length")) {
                String value = line.substring(colon + 1).trim();
                if (length != -1 || !value.matches("[0-9]{1,7}")) throw new IOException("invalid content length");
                length = Integer.parseInt(value);
            }
        }
        if (length < 2 || length > MAX_ROW) throw new IOException("response exceeds limit");
        byte[] body = input.readNBytes(length);
        if (body.length != length) throw new IOException("truncated response");
        JsonNode row = JSON.readTree(body);
        if (row == null || !row.isObject()) throw new IOException("invalid output object");
        return new String(body, StandardCharsets.UTF_8);
    }
}
