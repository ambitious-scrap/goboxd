# Unbounded stdout — io.LimitReader drains to Discard; wall time fires.
while True:
    print('A' * 1000)
