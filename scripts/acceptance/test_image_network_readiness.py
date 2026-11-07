"""Deterministic read-only readiness tests; no Docker or network mutation."""
from types import SimpleNamespace
import socket
import struct
import unittest
from unittest.mock import Mock, patch

import image_network_readiness as network
import run_image_app as app


class Clock:
    now = 0.0

    def monotonic(self):
        return self.now


class QuietWindowTests(unittest.TestCase):
    def observe(self, events=(), *, deadline=30, peer=(0, 0), flags=0):
        clock = Clock()
        events = list(events)
        sock = Mock()
        sock.recvmsg.return_value = (struct.pack('=IHHII', 36, 20, 0, 0, 0) + b'private-network-data', [], flags, peer)

        def select(read, write, error, timeout):
            self.assertEqual((read, write, error), ([sock], [], []))
            self.assertGreater(timeout, 0)
            self.assertLessEqual(timeout, .25)
            clock.now += timeout
            if events and events[0] <= clock.now:
                events.pop(0)
                return [sock], [], []
            return [], [], []

        guard = network.LinkAddressQuietWindow()
        guard.socket = sock
        with patch.object(network.time, 'monotonic', clock.monotonic), \
             patch.object(network.select, 'select', select):
            result = guard.wait(deadline=deadline)
        return result, sock

    def test_quiet_interval_observed_and_no_private_payload_returned(self):
        result, sock = self.observe()
        self.assertEqual(result, {'quietMilliseconds': 3000, 'observedMilliseconds': 3000, 'eventDatagrams': 0})
        sock.recvmsg.assert_not_called()

    def test_each_delayed_event_resets_full_quiet_window(self):
        result, sock = self.observe([.5, 2.75, 5.5])
        self.assertEqual(result, {'quietMilliseconds': 3000, 'observedMilliseconds': 8500, 'eventDatagrams': 3})
        self.assertNotIn('private', str(result))
        self.assertEqual(sock.recvmsg.call_count, 3)

    def test_never_finishes_when_event_stream_exceeds_fixed_deadline(self):
        with self.assertRaisesRegex(RuntimeError, 'deadline'):
            self.observe(range(1, 30))

    def test_respects_shorter_remaining_work_deadline_and_exact_boundary(self):
        for deadline in (0, 1, 3):
            with self.subTest(deadline=deadline), self.assertRaisesRegex(RuntimeError, 'deadline'):
                self.observe(deadline=deadline)

    def test_packet_ceiling_prevents_event_flood(self):
        with patch.object(network, 'MAX_DATAGRAMS', 2):
            with self.assertRaisesRegex(RuntimeError, 'event bound'):
                self.observe([.25, .5, .75])

    def test_truncated_or_non_kernel_messages_fail_closed(self):
        for options in ({'flags': socket.MSG_TRUNC}, {'peer': (7, 0)}, {'peer': ()}):
            with self.subTest(options=options), self.assertRaisesRegex(RuntimeError, 'incomplete'):
                self.observe([.25], **options)

    def test_kernel_error_overrun_unknown_and_malformed_packets_fail_closed(self):
        for packet in (b'', b'short', struct.pack('=IHHII', 16, 2, 0, 0, 0),
                       struct.pack('=IHHII', 16, 4, 0, 0, 0), struct.pack('=IHHII', 16, 99, 0, 0, 0),
                       struct.pack('=IHHII', 20, 20, 0, 0, 0), struct.pack('=IHHII', 15, 20, 0, 0, 0)):
            with self.subTest(packet=packet):
                with self.assertRaisesRegex(RuntimeError, 'incomplete'):
                    network.validate_notifications(packet)
        network.validate_notifications(struct.pack('=IHHII', 16, 16, 0, 0, 0) +
                                       struct.pack('=IHHII', 16, 21, 0, 0, 0))

    def test_subscription_uses_only_kernel_route_receive_socket_and_closes(self):
        sock = Mock()
        with patch.object(network.socket, 'socket', return_value=sock) as factory:
            with network.LinkAddressQuietWindow():
                factory.assert_called_once_with(socket.AF_NETLINK, socket.SOCK_DGRAM, socket.NETLINK_ROUTE)
                sock.bind.assert_called_once_with((0, 0x113))
                sock.setblocking.assert_called_once_with(False)
                sock.send.assert_not_called()
                sock.sendto.assert_not_called()
            sock.close.assert_called_once()

    def test_subscription_and_read_failures_fail_closed_without_raw_error(self):
        sock = Mock()
        sock.bind.side_effect = OSError('private-address')
        with patch.object(network.socket, 'socket', return_value=sock):
            with self.assertRaisesRegex(RuntimeError, '^image network readiness subscription unavailable$'):
                with network.LinkAddressQuietWindow():
                    self.fail('must not admit container creation')
        sock.close.assert_called_once()
        guard = network.LinkAddressQuietWindow()
        guard.socket = sock
        with patch.object(network.select, 'select', side_effect=OSError('private-address')):
            with self.assertRaisesRegex(RuntimeError, '^image network readiness observation failed$'):
                guard.wait(deadline=network.time.monotonic() + 30)


class ImageControlBoundaryTests(unittest.TestCase):
    def fixture(self, calls):
        handle = Mock()
        handle.poll.return_value = None
        runtime = SimpleNamespace(deadline=123, create=lambda *a, **k: calls.append('create') or handle)
        fixture = SimpleNamespace(runtime=runtime, control_secret_key='synthetic',
                                  write_private=Mock(return_value='private-file'), config=Mock(), state=Mock(),
                                  probe=Mock(return_value=200), note=Mock())
        def ready(*args):
            calls.append('http-ready')
            self.assertEqual(args[2], handle)
            self.assertTrue(args[1]())
        fixture.wait_until = ready
        return fixture, handle

    def test_subscribe_before_creation_and_recheck_readiness_after_quiet(self):
        calls = []
        fixture, handle = self.fixture(calls)
        guard = Mock()
        guard.__enter__ = Mock(side_effect=lambda: calls.append('subscribe') or guard)
        guard.__exit__ = Mock(side_effect=lambda *a: calls.append('close'))
        def wait(**kwargs):
            calls.append('network-ready')
            self.assertEqual(kwargs['deadline'], 123)
            return {'quietMilliseconds': 3000, 'eventDatagrams': 1, 'observedMilliseconds': 3250}
        guard.wait.side_effect = wait
        with patch.object(app, 'LinkAddressQuietWindow', return_value=guard):
            self.assertIs(app.ImageFixture.start_control(fixture, 'control-inbox-restart'), handle)
        self.assertEqual(calls, ['subscribe', 'create', 'http-ready', 'network-ready', 'close'])
        self.assertEqual(fixture.probe.call_count, 2)
        handle.poll.assert_called_once()

    def test_network_failure_cannot_return_control_or_emit_ready_message(self):
        calls = []
        fixture, _ = self.fixture(calls)
        guard = Mock()
        guard.__enter__ = Mock(return_value=guard)
        guard.__exit__ = Mock(return_value=False)
        guard.wait.side_effect = RuntimeError('image network readiness deadline exceeded')
        with patch.object(app, 'LinkAddressQuietWindow', return_value=guard):
            with self.assertRaisesRegex(RuntimeError, 'deadline'):
                app.ImageFixture.start_control(fixture, 'control-inbox-restart')
        fixture.note.assert_not_called()
        guard.__exit__.assert_called_once()

    def test_post_window_control_exit_and_lost_http_readiness_fail(self):
        for exited in (True, False):
            calls = []
            fixture, handle = self.fixture(calls)
            guard = Mock()
            guard.__enter__ = Mock(return_value=guard)
            guard.__exit__ = Mock(return_value=False)
            guard.wait.return_value = {'quietMilliseconds': 3000, 'eventDatagrams': 0, 'observedMilliseconds': 3000}
            if exited:
                handle.poll.return_value = 1
            else:
                fixture.probe.side_effect = (200, 503)
            with self.subTest(exited=exited), patch.object(app, 'LinkAddressQuietWindow', return_value=guard):
                with self.assertRaisesRegex(RuntimeError, 'exited|lost readiness'):
                    app.ImageFixture.start_control(fixture, 'control-inbox-restart')
            fixture.note.assert_not_called()


if __name__ == '__main__':
    unittest.main()
