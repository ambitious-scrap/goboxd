# Write a single file past rlimit_fsize (100 MB) — SIGXFSZ must terminate.
with open('/bigfile', 'wb') as f:
    while True:
        f.write(b'A' * (1024 * 1024))
