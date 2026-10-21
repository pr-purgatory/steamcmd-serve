#!/bin/sh
# install <appid> ...: appid 666 fails, anything else "installs".
# run [--workdir DIR] -- CMD...: exec CMD.
set -e
case "$1" in
  install)
    echo " Update state (0x61) downloading, progress: 50.00 (5 / 10)"
    if [ "$2" = 666 ]; then echo "ERROR! Failed to install app '666' (No subscription)"; exit 8; fi
    echo "built" > /server/fake_server
    echo "Success! App '$2' fully installed."
    ;;
  run)
    shift
    while [ "$1" != "--" ]; do shift; done
    shift
    cd /server
    exec "$@"
    ;;
  *) exit 64 ;;
esac
