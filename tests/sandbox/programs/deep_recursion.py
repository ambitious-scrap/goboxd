# Unbounded recursion — Python raises RecursionError and exits non-zero.
def f():
    return f()
f()
