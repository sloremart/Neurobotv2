FROM alpine:3.24.4

RUN apk add --no-cache bash curl jq docker-cli

COPY healthwatch.sh /usr/local/bin/healthwatch.sh
RUN chmod +x /usr/local/bin/healthwatch.sh

ENTRYPOINT ["/usr/local/bin/healthwatch.sh"]
