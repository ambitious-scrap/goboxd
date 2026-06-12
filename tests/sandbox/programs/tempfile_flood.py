# Create temp files without limit — wall time must stop this.
import tempfile
while True:
    tempfile.NamedTemporaryFile(delete=False, dir='/')
