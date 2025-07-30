# Etapa de build
FROM golang:1.24 AS builder

WORKDIR /app

# Copia os arquivos do projeto
COPY . .

# Compilando o binário server.go 
#Atenção: estamos colocando apenas o programa server.go no container, o tui.go continuará fora!
RUN go build -o rsyncuptime server.go

# Etapa final:
FROM debian:bookworm-slim

# Instalando o rsync:
RUN apt-get update && \
    apt-get install -y rsync && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# Copiando o binário compilado:
COPY --from=builder /app/rsyncuptime /usr/local/bin/rsyncuptime

# Exponha a porta, se necessário: 
#Atenção: se você mudar de porta, esse trecho do código deve-se mudar também!
EXPOSE 8080

# Definindo o comando de entrada:
CMD ["rsyncuptime"]
