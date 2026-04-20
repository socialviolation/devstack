FROM eclipse-temurin:17-jre-alpine

RUN apk add --no-cache curl

WORKDIR /app

RUN curl -fsSL -o clickhouse-jdbc-bridge.jar \
    https://repo1.maven.org/maven2/ru/yandex/clickhouse/clickhouse-jdbc-bridge/2.0.7/clickhouse-jdbc-bridge-2.0.7-shaded.jar

RUN mkdir -p /app/drivers && \
    curl -fsSL -o /app/drivers/postgresql-42.7.3.jar \
        https://jdbc.postgresql.org/download/postgresql-42.7.3.jar && \
    curl -fsSL -o /app/drivers/mssql-jdbc-12.8.1.jre11.jar \
        "https://github.com/microsoft/mssql-jdbc/releases/download/v12.8.1/mssql-jdbc-12.8.1.jre11.jar"

EXPOSE 9019
ENTRYPOINT ["java", "-jar", "/app/clickhouse-jdbc-bridge.jar"]
