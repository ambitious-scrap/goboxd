# Outbound connect — new network namespace has no interfaces; must fail ENETUNREACH.
import socket
socket.create_connection(('8.8.8.8', 53), timeout=1)
