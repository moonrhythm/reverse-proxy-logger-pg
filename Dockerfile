FROM golang:1.20.7

ENV CGO_ENABLED=0
ENV GOAMD64=v3

WORKDIR /workspace
ADD go.mod go.sum ./
RUN go mod download
ADD . .
RUN go build -o .build/reverse-proxy-logger-pg -ldflags "-w -s" .

FROM gcr.io/distroless/static

WORKDIR /app

COPY --from=0 /workspace/.build/* ./
ENTRYPOINT ["/app/reverse-proxy-logger-pg"]
