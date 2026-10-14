package io.github.solinode.homecloud.testcontainers;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.util.Map;
import org.junit.jupiter.api.Test;

class ParseAwsEnvTest {

    @Test
    void parsesQuotedExports() {
        Map<String, String> env = HomeCloudContainer.parseAwsEnv(
                "export AWS_ENDPOINT_URL=\"http://127.0.0.1:18080\"\n"
                        + "export AWS_ACCESS_KEY_ID=\"AKIAEXAMPLE\"\n"
                        + "export AWS_SECRET_ACCESS_KEY=\"a/b+c\\\"d\"\n"
                        + "export AWS_REGION=\"us-east-1\"\n"
                        + "# comment\n");
        assertEquals("AKIAEXAMPLE", env.get("AWS_ACCESS_KEY_ID"));
        assertEquals("a/b+c\"d", env.get("AWS_SECRET_ACCESS_KEY"));
        assertEquals("us-east-1", env.get("AWS_REGION"));
        assertEquals("http://127.0.0.1:18080", env.get("AWS_ENDPOINT_URL"));
    }

    @Test
    void namesALoopbackHostLocalhost() {
        assertEquals("localhost", HomeCloudContainer.endpointHost("localhost"));
        assertEquals("localhost", HomeCloudContainer.endpointHost("127.0.0.1"));
        assertEquals("docker.example", HomeCloudContainer.endpointHost("docker.example"));
    }
}
