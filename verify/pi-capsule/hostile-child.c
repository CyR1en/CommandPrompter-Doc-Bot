#define _GNU_SOURCE

#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <signal.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <sys/un.h>
#include <unistd.h>

extern char **environ;

static int status_value(pid_t pid, const char *name, unsigned long long *value) {
    char path[64];
    char line[256];
    snprintf(path, sizeof(path), "/proc/%d/status", pid);
    FILE *file = fopen(path, "r");
    if (file == NULL) return -1;
    while (fgets(line, sizeof(line), file) != NULL) {
        if (strncmp(line, name, strlen(name)) == 0) {
            *value = strtoull(line + strlen(name), NULL, 16);
            fclose(file);
            return 0;
        }
    }
    fclose(file);
    return -1;
}

static int process_counts(uid_t uid, int *zombies) {
    DIR *directory = opendir("/proc");
    if (directory == NULL) return -1;
    int count = 0;
    *zombies = 0;
    struct dirent *entry;
    while ((entry = readdir(directory)) != NULL) {
        char *end = NULL;
        long pid = strtol(entry->d_name, &end, 10);
        if (pid <= 0 || *end != '\0') continue;
        char path[64];
        snprintf(path, sizeof(path), "/proc/%ld/status", pid);
        FILE *file = fopen(path, "r");
        if (file == NULL) continue;
        char line[256];
        int matched = 0;
        int zombie = 0;
        while (fgets(line, sizeof(line), file) != NULL) {
            unsigned found;
            if (sscanf(line, "Uid:\t%u", &found) == 1 && found == uid) matched = 1;
            if (strncmp(line, "State:\tZ", 8) == 0) zombie = 1;
        }
        fclose(file);
        if (matched) {
            count++;
            if (zombie) (*zombies)++;
        }
    }
    closedir(directory);
    return count;
}

static int path_errno(const char *path, int operation) {
    errno = 0;
    if (operation == 0) {
        struct stat status;
        if (stat(path, &status) == 0) return 0;
    } else if (operation == 1) {
        int fd = open(path, O_WRONLY | O_CREAT | O_EXCL, 0600);
        if (fd >= 0) {
            close(fd);
            unlink(path);
            return 0;
        }
    } else if (operation == 2) {
        if (unlink(path) == 0) return 0;
    }
    return errno;
}

static int connect_errno(void) {
    int fd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) return errno;
    struct sockaddr_un address = {.sun_family = AF_UNIX};
    strncpy(address.sun_path, "/run/capsule/capsule.sock", sizeof(address.sun_path) - 1);
    errno = 0;
    int result = connect(fd, (struct sockaddr *)&address, sizeof(address));
    int saved = result == 0 ? 0 : errno;
    close(fd);
    return saved;
}

int main(void) {
    setvbuf(stdout, NULL, _IONBF, 0);
    gid_t groups[16];
    int group_count = getgroups(16, groups);
    unsigned long long cap_eff = 1;
    (void)status_value(getpid(), "CapEff:", &cap_eff);
    unsigned long long pid1_cap_eff = 0;
    unsigned long long pid1_uid = 1;
    unsigned long long pid1_no_new_privs = 0;
    (void)status_value(1, "CapEff:", &pid1_cap_eff);
    (void)status_value(1, "Uid:", &pid1_uid);
    (void)status_value(1, "NoNewPrivs:", &pid1_no_new_privs);
    errno = 0;
    int kill_result = kill(1, SIGTERM);
    int kill_errno = kill_result == 0 ? 0 : errno;
    int initial_zombies = 0;
    int initial_uid_processes = process_counts(getuid(), &initial_zombies);
    int environment_count = 0;
    int sensitive_environment = 0;
    for (char **value = environ; *value != NULL; value++) {
        environment_count++;
        if (strstr(*value, "TOKEN") != NULL || strstr(*value, "SECRET") != NULL ||
            strstr(*value, "PASSWORD") != NULL || strstr(*value, "MASTER_KEY") != NULL ||
            strstr(*value, "DATABASE") != NULL || strstr(*value, "POSTGRES") != NULL) {
            sensitive_environment++;
        }
    }
    const char *forbidden[] = {"/app", "/app/data", "/workspace", "/project",
                               "/var/run/docker.sock", "/run/postgresql/.s.PGSQL.5432"};
    int forbidden_paths_present = 0;
    for (size_t index = 0; index < sizeof(forbidden) / sizeof(forbidden[0]); index++) {
        if (access(forbidden[index], F_OK) == 0) forbidden_paths_present++;
    }

    int ready[2];
    if (pipe(ready) != 0) return 2;
    pid_t first = fork();
    if (first < 0) return 3;
    if (first == 0) {
        close(ready[0]);
        if (setsid() < 0) _exit(4);
        pid_t second = fork();
        if (second < 0) _exit(5);
        if (second == 0) {
            for (;;) pause();
        }
        (void)write(ready[1], "1", 1);
        _exit(0); /* Deliberately becomes a zombie until cleanup. */
    }
    close(ready[1]);
    char marker = 0;
    if (read(ready[0], &marker, 1) != 1) return 6;
    close(ready[0]);
    usleep(50000);
    int escaped_zombies = 0;
    int escaped_uid_processes = process_counts(getuid(), &escaped_zombies);

    printf("{\"pid\":%d,\"uid\":%d,\"gid\":%d,\"groups\":%d,"
           "\"cap_eff\":%llu,\"no_new_privs\":%d,"
           "\"pid1_uid\":%llu,\"pid1_cap_eff\":%llu,\"pid1_no_new_privs\":%llu,"
           "\"kill_pid1_result\":%d,\"kill_pid1_errno\":%d,"
           "\"socket_stat_errno\":%d,\"socket_write_errno\":%d,"
           "\"socket_unlink_errno\":%d,\"socket_connect_errno\":%d,"
           "\"root_write_errno\":%d,\"tmp_write_errno\":%d,"
           "\"dev_shm_write_errno\":%d,\"home_exists\":%s,\"tmpdir_exists\":%s,"
           "\"environment_count\":%d,\"sensitive_environment\":%d,"
           "\"forbidden_paths_present\":%d,"
           "\"initial_uid_processes\":%d,\"initial_zombies\":%d,"
           "\"escaped_uid_processes\":%d,\"escaped_zombies\":%d,"
           "\"setsid_double_fork_created\":true}\n",
           getpid(), getuid(), getgid(), group_count, cap_eff,
           prctl(PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0), pid1_uid, pid1_cap_eff,
           pid1_no_new_privs, kill_result, kill_errno,
           path_errno("/run/capsule/capsule.sock", 0),
           path_errno("/run/capsule/hostile-write", 1),
           path_errno("/run/capsule/capsule.sock", 2), connect_errno(),
           path_errno("/hostile-write", 1), path_errno("/tmp/hostile-write", 1),
           path_errno("/dev/shm/hostile-write", 1),
           access(getenv("HOME"), F_OK) == 0 ? "true" : "false",
           access(getenv("TMPDIR"), F_OK) == 0 ? "true" : "false",
           environment_count, sensitive_environment, forbidden_paths_present,
           initial_uid_processes, initial_zombies, escaped_uid_processes, escaped_zombies);

    char line[32];
    while (fgets(line, sizeof(line), stdin) != NULL) {
        printf("{\"pong\":true,\"pid\":%d}\n", getpid());
    }
    return 0;
}
