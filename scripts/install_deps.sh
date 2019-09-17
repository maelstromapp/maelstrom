#!/bin/bash -x

# install barrister
cmd=$(command -v barrister)
if [ -z "$cmd" ]; then
    echo "Installing barrister"
    set -e
#    opts="--user"
#    if [ -n "$CI_CACHEDIR" ]; then
#        apt-get update
#        apt-get install -y python-setuptools
#        easy_install pip
#        opts="--install-option=--prefix=$CI_CACHEDIR --ignore-installed"
#        pip install $opts setuptools
#    fi
    pip install --pre --user barrister
    set +e
else
    echo "Found barrister: $cmd"
fi

# install barrister-go
cmd=$(command -v idl2go)
if [ -z "$cmd" ]; then
    echo "Installing barrister-go / idl2go"
    set -e
    go get github.com/coopernurse/barrister-go
    go install github.com/coopernurse/barrister-go/idl2go
    set +e
else
    echo "Found idl2go: $cmd"
fi

# install errcheck
cmd=$(command -v errcheck)
if [ -z "$cmd" ]; then
    echo "Installing errcheck"
    set -e
    go get github.com/kisielk/errcheck
    set +e
else
    echo "Found errcheck: $cmd"
fi
