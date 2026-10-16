FROM golang:1.26-alpine AS build
WORKDIR /src
# The context is the services/ directory: the replace dependencies ../cbl-kit and ../cbl-protos need the neighboring modules.
COPY cbl-kit/go.mod cbl-kit/go.sum ./cbl-kit/
COPY cbl-protos/go.mod cbl-protos/go.sum ./cbl-protos/
COPY cbl-backoffice/go.mod cbl-backoffice/go.sum ./cbl-backoffice/
RUN cd cbl-backoffice && go mod download
COPY cbl-kit ./cbl-kit
COPY cbl-protos ./cbl-protos
COPY cbl-backoffice ./cbl-backoffice
RUN cd cbl-backoffice && CGO_ENABLED=0 go build -trimpath -o /out/stubs ./test/e2e/.additionals/stubs

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stubs /app/stubs
ENTRYPOINT ["/app/stubs"]
