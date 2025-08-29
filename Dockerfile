# START CONTAINER
# docker build -t cni-test .
# Start in privileged mode in order to create namespaces:
# docker run --privileged -it --rm cni-test

# TEST USING cnitool
# echo '{"cniVersion":"0.4.0","name":"host-vrf-test","type":"ptp","plugins":[{"type":"ptp", "ipam":{"type":"host-local","subnet":"172.16.29.0/24"}}, {"type":"hostvrf","vrfname":"vrf-blue", "table": 10}]}' | jq . | tee /etc/cni/net.d/10-testnet.conflist
# ip link add vrf-blue type vrf table 10
# ip netns add testing
# CNI_PATH=./bin cnitool add host-vrf-test /var/run/netns/testing

# RUN UNIT TEST
# ginkgo -v ./plugins/meta/hostvrf/

FROM golang:1.25

# Install test dependencies and utily tools
RUN apt-get update && apt-get install -y \
    iproute2 \
    iptables \
    iputils-ping \
    net-tools \
    jq \
    vim \
    less \
    && rm -rf /var/lib/apt/lists/*

RUN /usr/local/go/bin/go install github.com/onsi/ginkgo/v2/ginkgo@v2.23.4

# Set workdir to repo root
WORKDIR /cni-plugins

# Copy the repo into the image
COPY . .

# Build all plugins by default (see Makefile/README)
RUN bash -x ./build_linux.sh

# Prepare directories for CNI and example config (optional)
RUN mkdir -p /opt/cni/bin /etc/cni/net.d

# Move built binaries to CNI dir
RUN cp ./bin/* /opt/cni/bin/

# Install cnitool for manual plugin testing
RUN go install github.com/containernetworking/cni/cnitool@latest && \
    cp /go/bin/cnitool /usr/local/bin/

# Set env for CNI tools
ENV CNI_PATH=/opt/cni/bin
ENV PATH=$PATH:/usr/local/bin:/opt/cni/bin

# By default, launch a shell for manual testing
CMD ["/bin/bash"]
