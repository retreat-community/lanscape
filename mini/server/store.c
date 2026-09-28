/* Run history: one JSON file, a JSON array with one run per line.
 * The server never parses JSON; lines are kept as opaque strings and only
 * the leading "id"/"started" fields are scanned. */
#include "server.h"
#include "../common/jw.h"
#include "../common/util.h"

#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define MAX_RUNS 200

typedef struct {
    uint32_t id;
    uint64_t started;
    char *json;
    size_t len;
} stored_t;

static stored_t runs[MAX_RUNS];
static int nruns;
static uint32_t next_id = 1;

static uint64_t field(const char *s, size_t len, const char *key)
{
    size_t kl = strlen(key);
    uint64_t v = 0;
    const char *p = s, *e = s + len;
    while (p + kl < e && memcmp(p, key, kl))
        p++;
    if (p + kl >= e)
        return 0;
    for (p += kl; p < e && *p >= '0' && *p <= '9'; p++)
        v = v * 10 + (uint64_t)(*p - '0');
    return v;
}

static void push(char *json, size_t len)
{
    stored_t *r;
    uint32_t keep = g_conf.keep_runs ? g_conf.keep_runs : 20;
    if (keep > MAX_RUNS)
        keep = MAX_RUNS;
    while ((uint32_t)nruns >= keep) {
        free(runs[0].json);
        memmove(runs, runs + 1, sizeof(runs[0]) * (size_t)(nruns - 1));
        nruns--;
    }
    r = &runs[nruns++];
    r->json = json;
    r->len = len;
    r->id = (uint32_t)field(json, len, "\"id\":");
    r->started = field(json, len, "\"started\":");
    if (r->id >= next_id)
        next_id = r->id + 1;
}

void store_load(void)
{
    FILE *f = fopen(g_conf.data_file, "r");
    char *line = NULL;
    size_t cap = 0;
    ssize_t n;
    if (!f)
        return;
    while ((n = getline(&line, &cap, f)) > 0) {
        char *copy;
        while (n && (line[n - 1] == '\n' || line[n - 1] == '\r' || line[n - 1] == ','))
            line[--n] = 0;
        if (n < 2 || line[0] != '{')
            continue;
        copy = malloc((size_t)n + 1);
        if (!copy)
            break;
        memcpy(copy, line, (size_t)n + 1);
        push(copy, (size_t)n);
    }
    free(line);
    fclose(f);
    ls_log(LOG_I, "loaded %d runs from %s", nruns, g_conf.data_file);
}

static void save(void)
{
    char tmp[300];
    int fd, i, ok = 1;
    ls_fmt(tmp, sizeof(tmp), "%s.tmp", g_conf.data_file);
    fd = open(tmp, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0644);
    if (fd < 0) {
        ls_log(LOG_W, "cannot write %s", tmp);
        return;
    }
    ok &= write(fd, "[\n", 2) == 2;
    for (i = 0; i < nruns && ok; i++) {
        ok &= write(fd, runs[i].json, runs[i].len) == (ssize_t)runs[i].len;
        {
            const char *sep = i + 1 < nruns ? ",\n" : "\n";
            ok &= write(fd, sep, strlen(sep)) == (ssize_t)strlen(sep);
        }
    }
    ok &= write(fd, "]\n", 2) == 2;
    ok &= fsync(fd) == 0;
    close(fd);
    if (!ok || rename(tmp, g_conf.data_file)) {
        ls_log(LOG_W, "cannot save runs to %s", g_conf.data_file);
        unlink(tmp);
    }
}

void store_add(char *json, size_t len)
{
    push(json, len);
    save();
}

const char *store_last(size_t *len)
{
    if (!nruns)
        return NULL;
    *len = runs[nruns - 1].len;
    return runs[nruns - 1].json;
}

const char *store_get(uint32_t id, size_t *len)
{
    int i;
    for (i = 0; i < nruns; i++)
        if (runs[i].id == id) {
            *len = runs[i].len;
            return runs[i].json;
        }
    return NULL;
}

uint32_t store_next_id(void) { return next_id++; }

uint32_t store_last_id(void) { return nruns ? runs[nruns - 1].id : 0; }

void store_list_json(void *wp)
{
    jw *w = wp;
    int i;
    jw_arr(w);
    for (i = nruns - 1; i >= 0; i--) {
        jw_obj(w);
        jw_kuint(w, "id", runs[i].id);
        jw_kuint(w, "started", runs[i].started);
        jw_end_obj(w);
    }
    jw_end_arr(w);
}
