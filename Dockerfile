# syntax=docker/dockerfile:1

# ---- Build stage -------------------------------------------------------------
FROM golang:1.25-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

# Dependencies first, so source changes don't invalidate the module cache layer
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/main ./cmd/server

# ---- Runtime stage -----------------------------------------------------------
FROM alpine:3.22

WORKDIR /app

# ca-certificates: outbound TLS (Documanta, SMTP, SSO)
# tzdata:          Asia/Riyadh and other zones for scheduled jobs
# chromium + fonts: headless PDF rendering of reports (CHROME_BIN below);
#                   font-noto-arabic is needed for Arabic text in PDFs
RUN apk add --no-cache \
      ca-certificates \
      tzdata \
      wget \
      chromium \
      nss \
      freetype \
      harfbuzz \
      ttf-freefont \
      font-noto \
      font-noto-arabic \
      dbus \
 && addgroup -S app && adduser -S -G app -h /app app

COPY --from=builder /out/main /app/main

# Report PDF generation calls this binary (config: CHROME_BIN, default google-chrome)
ENV CHROME_BIN=/usr/bin/chromium-browser \
    TZ=Asia/Riyadh

# Firebase credentials are NOT baked into the image. To enable push notifications,
# mount the JSON file and set FIREBASE_CREDENTIAL_PATH to its path inside the container.

USER app

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD wget -qO- http://127.0.0.1:${SERVER_PORT:-8080}/api/v1/health >/dev/null || exit 1

CMD ["./main"]
