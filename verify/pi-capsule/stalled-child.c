#include <arpa/inet.h>
#include <stdint.h>
#include <unistd.h>

static int write_all(int fd, const void *buffer, size_t length) {
    size_t offset = 0;
    while (offset < length) {
        ssize_t count = write(fd, (const char *)buffer + offset, length - offset);
        if (count <= 0) return -1;
        offset += (size_t)count;
    }
    return 0;
}

static int read_all(int fd, void *buffer, size_t length) {
    size_t offset = 0;
    while (offset < length) {
        ssize_t count = read(fd, (char *)buffer + offset, length - offset);
        if (count <= 0) return -1;
        offset += (size_t)count;
    }
    return 0;
}

int main(void) {
    uint32_t start_length = 0;
    if (read_all(STDIN_FILENO, &start_length, sizeof(start_length)) != 0) return 2;
    start_length = ntohl(start_length);
    if (start_length < 1 || start_length > 65536) return 3;
    char chunk[8192];
    uint32_t remaining = start_length;
    while (remaining > 0) {
        size_t selected = remaining < sizeof(chunk) ? remaining : sizeof(chunk);
        if (read_all(STDIN_FILENO, chunk, selected) != 0) return 4;
        remaining -= (uint32_t)selected;
    }

    static const char request[] =
        "{\"type\":\"model_request\",\"id\":\"stalled-model\",\"turn\":1}";
    uint32_t network_length = htonl((uint32_t)(sizeof(request) - 1));
    if (write_all(STDOUT_FILENO, &network_length, sizeof(network_length)) != 0 ||
        write_all(STDOUT_FILENO, request, sizeof(request) - 1) != 0) return 5;

    uint32_t result_length = 0;
    if (read_all(STDIN_FILENO, &result_length, sizeof(result_length)) != 0) return 6;
    result_length = ntohl(result_length);
    if (result_length < 900000 || result_length > 2097152) return 7;
    remaining = result_length;
    while (remaining > 0) {
        size_t selected = remaining < sizeof(chunk) ? remaining : sizeof(chunk);
        if (read_all(STDIN_FILENO, chunk, selected) != 0) return 8;
        remaining -= (uint32_t)selected;
    }

    sleep(3);
    return 0;
}
