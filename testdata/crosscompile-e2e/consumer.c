#include <stdio.h>
#include <string.h>
#include <zlib.h>

int main(void) {
    static const Bytef input[] = "LLAR cross-compiled zlib";
    static const char gzip_path[] = "llar-zlib-e2e.gz";
    Bytef compressed[128];
    Bytef restored[128];
    uLongf compressed_len = sizeof(compressed);
    uLongf restored_len = sizeof(restored);
    gzFile gzip;
    int gzip_len;

    if (compress(compressed, &compressed_len, input, sizeof(input)) != Z_OK) {
        return 1;
    }
    if (uncompress(restored, &restored_len, compressed, compressed_len) != Z_OK) {
        return 2;
    }
    if (restored_len != sizeof(input) || memcmp(restored, input, sizeof(input)) != 0) {
        return 3;
    }
    if (strcmp(zlibVersion(), ZLIB_VERSION) != 0) {
        return 4;
    }

    gzip = gzopen(gzip_path, "wb");
    if (gzip == NULL) {
        return 5;
    }
    if (gzwrite(gzip, input, (unsigned)sizeof(input)) != (int)sizeof(input)) {
        return 6;
    }
    if (gzclose(gzip) != Z_OK) {
        return 7;
    }

    gzip = gzopen(gzip_path, "rb");
    if (gzip == NULL) {
        return 8;
    }
    gzip_len = gzread(gzip, restored, (unsigned)sizeof(restored));
    if (gzip_len != (int)sizeof(input)) {
        return 9;
    }
    if (gzclose(gzip) != Z_OK) {
        return 10;
    }
    if (memcmp(restored, input, sizeof(input)) != 0) {
        return 11;
    }
    if (remove(gzip_path) != 0) {
        return 12;
    }

    printf("zlib %s: compressed %lu bytes to %lu bytes and verified gzip I/O\n",
        zlibVersion(), (unsigned long)sizeof(input), (unsigned long)compressed_len);
    return 0;
}
