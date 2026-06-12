# os.chroot() requires CAP_SYS_CHROOT — must raise PermissionError.
import os
os.chroot('/')
