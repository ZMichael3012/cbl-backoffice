FROM golang:1.26-alpine AS build
WORKDIR /src
# The context is the services/ directory: the replace dependency ../cbl-kit needs the neighboring module.
COPY cbl-kit/go.mod cbl-kit/go.sum ./cbl-kit/
COPY cbl-backoffice/go.mod cbl-backoffice/go.sum ./cbl-backoffice/
RUN cd cbl-backoffice && go mod download
COPY cbl-kit ./cbl-kit
COPY cbl-backoffice ./cbl-backoffice
RUN cd cbl-backoffice && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=docker" -o /out/backoffice ./cmd/backoffice

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/backoffice /app/backoffice
# Migrations are applied by the service at startup (MIGRATIONS_DIR).
COPY --from=build /src/cbl-backoffice/migrations /app/migrations
EXPOSE 8020 9220
ENTRYPOINT ["/app/backoffice"]
