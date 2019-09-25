# build stage
FROM debian:stretch-slim AS build-env
RUN apt-get update && apt install -y ca-certificates
RUN apt install -y curl python-pip make
RUN cd /usr/local && curl -LO https://dl.google.com/go/go1.13.linux-amd64.tar.gz && \
    tar zxf go1.13.linux-amd64.tar.gz && rm -f go1.13.linux-amd64.tar.gz
RUN pip install --pre barrister
RUN apt install -y git libsqlite3-dev
ENV GOROOT=/usr/local/go
ENV PATH="${GOROOT}/bin:/root/go/bin:${PATH}"
RUN go get github.com/coopernurse/barrister-go && go install github.com/coopernurse/barrister-go/idl2go
ADD . /src
RUN cd /src && make idl && make maelstromd && make maelctl

# final stage
FROM debian:stretch-slim
RUN apt-get update && apt install -y ca-certificates libsqlite3-dev
WORKDIR /app
COPY --from=build-env /src/dist/maelstromd /usr/bin
COPY --from=build-env /src/dist/maelctl /usr/bin
