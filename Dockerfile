##################################
# Stage 0: Build frontend module
##################################

FROM node:20-alpine AS frontend-builder
RUN npm install -g pnpm@9
WORKDIR /frontend
COPY frontend/package.json frontend/pnpm-lock.yaml* ./
RUN pnpm install --frozen-lockfile || pnpm install
COPY frontend/ .
RUN pnpm build

##################################
# Stage 1: Build Go executable
##################################

FROM golang:1.25-alpine AS builder

ARG APP_VERSION=1.0.0
ENV GOTOOLCHAIN=auto

RUN apk add --no-cache git make curl
RUN curl -sSL "https://github.com/bufbuild/buf/releases/latest/download/buf-$(uname -s)-$(uname -m)" -o /usr/local/bin/buf && \
    chmod +x /usr/local/bin/buf

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Regenerate proto descriptor so the embedded descriptor.bin is always fresh.
RUN buf build -o cmd/server/assets/descriptor.bin

# Embed the built frontend.
COPY --from=frontend-builder /frontend/dist cmd/server/assets/frontend-dist/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags "-X main.version=${APP_VERSION} -s -w" \
    -o /src/bin/ticket-server \
    ./cmd/server

##################################
# Stage 2: Runtime image
##################################

FROM alpine:3.20

ARG APP_VERSION=1.0.0
RUN apk --no-cache add ca-certificates tzdata
ENV TZ=UTC
ENV GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn

WORKDIR /app
COPY --from=builder /src/bin/ticket-server /app/bin/ticket-server
COPY --from=builder /src/configs/ /app/configs/

RUN addgroup -g 1000 ticket && \
    adduser -D -u 1000 -G ticket ticket && \
    chown -R ticket:ticket /app
USER ticket:ticket

EXPOSE 10800 10801
CMD ["/app/bin/ticket-server", "-c", "/app/configs"]

LABEL org.opencontainers.image.title="Ticket Service" \
      org.opencontainers.image.description="Support ticket system (iris email ingest + assignment)" \
      org.opencontainers.image.version="${APP_VERSION}"
