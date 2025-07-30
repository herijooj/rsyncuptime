# Etapa de build
FROM golang:1.24 AS builder

WORKDIR /app

# Copia os arquivos do projeto
COPY . .

# Compila o binário
RUN go build -o rsyncuptime server.go

# Etapa final
FROM debian:bookworm-slim

# Instala o rsync
RUN apt-get update && \
    apt-get install -y rsync && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# Copia o binário compilado
COPY --from=builder /app/rsyncuptime /usr/local/bin/rsyncuptime

# Exponha a porta, se necessário
EXPOSE 8080

# Define o comando de entrada
CMD ["rsyncuptime"]
