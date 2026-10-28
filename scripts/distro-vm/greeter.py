#!/usr/bin/env python3
"""A stand-in for the greeter's kiosk: lys-login daemon starts it as the
compositor, and it speaks the greetd protocol on GREETD_SOCK the way the
greeter does. The first time it logs in with a wrong and then the right
password and starts the session, which stops it; once the session ends and the daemon
starts it again, it records that and waits to be stopped."""

import json
import os
import signal
import socket
import struct
import sys

OUT = "/run/lys-distro-vm"
SESSION = "/usr/local/bin/lys-distro-vm-session"


def send(sock, msg):
    data = json.dumps(msg).encode()
    sock.sendall(struct.pack("=I", len(data)) + data)


def recv_exactly(sock, n):
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise EOFError("the daemon closed the greetd socket")
        buf += chunk
    return buf


def recv(sock):
    (n,) = struct.unpack("=I", recv_exactly(sock, 4))
    return json.loads(recv_exactly(sock, n))


def record(name, line):
    with open(os.path.join(OUT, name), "a") as f:
        f.write(line + "\n")


def login(sock, username, password):
    send(sock, {"type": "create_session", "username": username})
    reply = recv(sock)
    while reply["type"] == "auth_message":
        wants = reply["auth_message_type"] in ("secret", "visible")
        send(sock, {"type": "post_auth_message_response", "response": password if wants else None})
        reply = recv(sock)
    return reply


def main():
    if os.path.exists(os.path.join(OUT, "greeter")):
        record("greeter-again", "up")
        signal.pause()
    with open(os.path.join(OUT, "credentials")) as f:
        username, password = f.read().split("\n")[:2]
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.connect(os.environ["GREETD_SOCK"])
    record("greeter", "wrong-password " + json.dumps(login(sock, username, "wrong-password")))
    record("greeter", "password " + json.dumps(login(sock, username, password)))
    send(sock, {"type": "start_session", "cmd": [SESSION], "env": []})
    signal.pause()


if __name__ == "__main__":
    sys.exit(main())
