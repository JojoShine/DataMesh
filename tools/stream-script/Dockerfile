ARG FLINK_BUILD_IMAGE=flink:1.20.3-scala_2.12-java17@sha256:6a2b771c073b523e43d72b46e338ce6cef25e969965444f6afe1b0bb15fa9772
ARG MAVEN_IMAGE=public.ecr.aws/docker/library/maven:3.9.9-eclipse-temurin-21@sha256:3a4ab3276a087bf276f79cae96b1af04f53731bec53fb2e651aca79e4b10211e
FROM ${FLINK_BUILD_IMAGE} AS flink
FROM ${MAVEN_IMAGE} AS compiler
ENV JAVA_TOOL_OPTIONS="-XX:ActiveProcessorCount=1 -Xmx192m"
COPY --from=flink /opt/flink/lib /opt/flink/lib
COPY src /src
RUN mkdir -p /build/classes /build/tests && \
    javac --release 17 -cp '/opt/flink/lib/*' -d /build/classes /src/main/java/datamesh/governance/*.java && \
    javac --release 17 -cp '/build/classes:/opt/flink/lib/*' -d /build/tests /src/test/java/datamesh/governance/*.java && \
    jar --create --file /build/datamesh-stream-script.jar -C /build/classes .
FROM flink AS verify
ENV JAVA_TOOL_OPTIONS="-XX:ActiveProcessorCount=1 -Xmx192m"
COPY --from=compiler /build /build
COPY verify.sh /build/verify.sh
RUN bash /build/verify.sh
FROM scratch
COPY --from=verify /build/datamesh-stream-script.jar /datamesh-stream-script.jar
