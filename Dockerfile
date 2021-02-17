FROM golang:1.15

ADD src/* /usr/src/crserver-proxy/

WORKDIR /usr/src/crserver-proxy

RUN ["go", "build"]


FROM debian:buster-slim
MAINTAINER Magister

ENV LISTEN_PORT 80
ENV REPO_URL http://foo/repo/repo.1ccr

EXPOSE 80

COPY --from=0 /usr/src/crserver-proxy/crserver-proxy /

CMD ["/crserver-proxy"]
