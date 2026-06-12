public class MemoryHog {
    public static void main(String[] args) throws InterruptedException {
        final int megabytes = readSize();
        final int blockSize = 1 << 20; // 1 MB per block
        final byte[][] blocks = new byte[megabytes][];

        long checksum = 0;

        // Allocate and touch every page so the pages are actually committed to RSS.
        for (int i = 0; i < megabytes; i++) {
            byte[] block = new byte[blockSize];
            for (int j = 0; j < blockSize; j += 4096) {
                block[j] = (byte) (i * 31 + j);
                checksum += block[j];
            }
            blocks[i] = block;
        }

        // Light CPU pass so a run is not pure allocation.
        for (byte[] block : blocks) {
            for (int j = 0; j < block.length; j += 512) {
                checksum += block[j];
            }
        }

        // Hold the memory resident for a moment so concurrent runs pile up.
        Thread.sleep(1000);

        // Deterministic output proves the run actually completed.
        System.out.println("MemoryHog OK mb=" + megabytes + " checksum=" + checksum);
    }

    private static int readSize() {
        String env = System.getenv("MEMHOG_MB");
        if (env != null && !env.isEmpty()) {
            try {
                return Integer.parseInt(env.trim());
            } catch (NumberFormatException ignored) {
            }
        }
        return 150;
    }
}
