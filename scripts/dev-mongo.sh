#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
DIR=$ROOT/.orqen-dev
PORT=${ORQEN_DEV_MONGO_PORT:-27018}

case "${1:-}" in
  start)
    mkdir -p "$DIR/mongo"
    if mongosh --port "$PORT" --quiet --eval 1 >/dev/null 2>&1; then
      echo "already running"
    else
      nohup mongod --replSet orqen-rs --port "$PORT" --bind_ip 127.0.0.1 --dbpath "$DIR/mongo" \
        --logpath "$DIR/mongo.log" >/dev/null 2>&1 &
      echo $! >"$DIR/mongo.pid"
      for _ in $(seq 1 30); do
        mongosh --port "$PORT" --quiet --eval 1 >/dev/null 2>&1 && break
        sleep 1
      done
    fi
    mongosh --port "$PORT" --quiet --eval 'try{rs.status().ok}catch(e){rs.initiate({_id:"orqen-rs",members:[{_id:0,host:"127.0.0.1:'"$PORT"'"}]})}' >/dev/null
    for _ in $(seq 1 30); do
      if [ "$(mongosh --port "$PORT" --quiet --eval 'db.hello().isWritablePrimary' 2>/dev/null)" = "true" ]; then
        echo "ready on 127.0.0.1:$PORT"
        exit 0
      fi
      sleep 1
    done
    echo "mongod did not become primary" >&2
    exit 1
    ;;
  stop)
    mongosh --port "$PORT" admin --quiet --eval 'db.shutdownServer()' >/dev/null 2>&1 || true
    ;;
  status)
    mongosh --port "$PORT" --quiet --eval 'print(rs.status().ok); print(db.hello().setName)'
    ;;
  reset)
    "$0" stop
    sleep 2
    rm -rf "$DIR/mongo"
    ;;
  *)
    echo "usage: $0 start|stop|status|reset" >&2
    exit 64
    ;;
esac
