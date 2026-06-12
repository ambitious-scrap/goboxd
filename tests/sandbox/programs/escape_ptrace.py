# ptrace is in the seccomp KILL_PROCESS deny-list — must deliver SIGKILL.
import ctypes
ctypes.CDLL(None).ptrace(0, 0, 0, 0)
