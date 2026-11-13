ARG MAVEN_IMAGE=public.ecr.aws/docker/library/maven:3.9.9-eclipse-temurin-21@sha256:3a4ab3276a087bf276f79cae96b1af04f53731bec53fb2e651aca79e4b10211e
ARG JAVA_IMAGE=public.ecr.aws/docker/library/eclipse-temurin:21-jre@sha256:0b99b3376fbb50dbf9c0af8535fdd5b8eb0d3fdfc8b1e4967f383b6919f6901a
FROM ${MAVEN_IMAGE} AS build
WORKDIR /build
COPY tools/source-reader/pom.xml .
RUN mvn -q dependency:go-offline
COPY tools/source-reader/src src
RUN mvn -q package dependency:copy-dependencies -DoutputDirectory=target/lib
FROM ${JAVA_IMAGE}
WORKDIR /app
COPY --from=build /build/target/classes /app/classes
COPY --from=build /build/target/lib /app/lib
USER 65532:65532
ENTRYPOINT ["java","-Xms32m","-Xmx384m","-cp","/app/classes:/app/lib/*:/opt/drivers/*","datamesh.source.Main"]
