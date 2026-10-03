# [DEMO] Builds client_java from CLIENT_JAVA_REF (main by default) and the example
# application against it, without installing Java on the host. Used by
# ../deploy.sh as:
#
#   docker build --build-arg CLIENT_JAVA_REF=<sha> --output type=local,dest=<dir> .
#
# which writes <dir>/app.jar (a fat jar) and <dir>/client_java.version.
FROM eclipse-temurin:25-jdk AS build
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends git >/dev/null && rm -rf /var/lib/apt/lists/*

ARG CLIENT_JAVA_REF=main
WORKDIR /client_java
RUN git init -q . && git remote add origin https://github.com/prometheus/client_java.git && \
    git fetch -q --depth 1 origin "${CLIENT_JAVA_REF}" && git checkout -q FETCH_HEAD
# Only the modules the example needs, without tests and checks, to keep the build short.
RUN ./mvnw -B -q install -DskipTests -Dcheckstyle.skip -Denforcer.skip -Djacoco.skip \
      -Dmaven.javadoc.skip -Dmaven.source.skip -Dgpg.skip \
      -pl prometheus-metrics-bom,prometheus-metrics-core,prometheus-metrics-instrumentation-jvm,prometheus-metrics-exporter-httpserver -am && \
    echo "$(./mvnw -B -q help:evaluate -Dexpression=project.version -DforceStdout) ($(git rev-parse --short=12 HEAD))" >/client_java.version

WORKDIR /app
COPY pom.xml .
COPY src ./src
RUN /client_java/mvnw -B -q package -Dprometheus.version="$(cut -d' ' -f1 /client_java.version)"

FROM scratch
COPY --from=build /app/target/app.jar /client_java.version /
