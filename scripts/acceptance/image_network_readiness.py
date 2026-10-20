"""Read-only Linux link/address quiescence for the disposable image harness.

Docker bridge recreation can publish delayed link/address events after HTTP is
ready. Chromium observes these events and debounces IP changes for two seconds.
This barrier is infrastructure readiness, not a retry or browser-error waiver.
No addresses, interface names, or raw kernel messages are retained or emitted.
"""
import select
import socket
import struct
import time

QUIET_SECONDS = 3.0
WAIT_SECONDS = 15.0
MAX_DATAGRAMS = 4096
MAX_DATAGRAM_BYTES = 65536
# Linux rtnetlink multicast groups: LINK, NOTIFY, IPv4 IFADDR, IPv6 IFADDR.
GROUPS = 0x1 | 0x2 | 0x10 | 0x100


def validate_notifications(data):
    """Accept only complete kernel link/address notifications, not loss/errors."""
    if not data:
        raise RuntimeError('image network readiness observation incomplete')
    offset = 0
    while offset < len(data):
        if len(data) - offset < 16:
            raise RuntimeError('image network readiness observation incomplete')
        length, kind, _, _, _ = struct.unpack_from('=IHHII', data, offset)
        if length < 16 or length > len(data) - offset or kind not in (16, 17, 20, 21):
            raise RuntimeError('image network readiness observation incomplete')
        offset += (length + 3) & ~3
    if offset != len(data):
        raise RuntimeError('image network readiness observation incomplete')


class LinkAddressQuietWindow:
    """Subscribe before owned container creation; fail closed on uncertainty."""

    def __init__(self):
        self.socket = None

    def __enter__(self):
        try:
            self.socket = socket.socket(socket.AF_NETLINK, socket.SOCK_DGRAM, socket.NETLINK_ROUTE)
            self.socket.bind((0, GROUPS))
            self.socket.setblocking(False)
            return self
        except (AttributeError, OSError):
            if self.socket is not None:
                self.socket.close()
            raise RuntimeError('image network readiness subscription unavailable') from None

    def __exit__(self, *_):
        self.socket.close()

    def wait(self, *, deadline):
        """Require an observed quiet interval within both fixed and work bounds."""
        started = time.monotonic()
        stop = min(deadline, started + WAIT_SECONDS)
        quiet_since = started
        datagrams = 0
        while True:
            now = time.monotonic()
            if now >= stop:
                raise RuntimeError('image network readiness deadline exceeded')
            try:
                readable, _, _ = select.select([self.socket], [], [], min(0.25, stop - now))
                if readable:
                    # Drain one bounded packet per iteration, so a continuous
                    # event stream cannot starve the deadline checks.
                    data, _, flags, peer = self.socket.recvmsg(MAX_DATAGRAM_BYTES)
                    if not data or flags & socket.MSG_TRUNC or not peer or peer[0] != 0:
                        raise RuntimeError('image network readiness observation incomplete')
                    validate_notifications(data)
                    datagrams += 1
                    if datagrams > MAX_DATAGRAMS:
                        raise RuntimeError('image network readiness event bound exceeded')
                    quiet_since = time.monotonic()
                    continue
            except (OSError, ValueError):
                raise RuntimeError('image network readiness observation failed') from None
            now = time.monotonic()
            if now >= stop:
                raise RuntimeError('image network readiness deadline exceeded')
            if now - quiet_since >= QUIET_SECONDS:
                return {'quietMilliseconds': int(QUIET_SECONDS * 1000),
                        'observedMilliseconds': int((now - started) * 1000),
                        'eventDatagrams': datagrams}
