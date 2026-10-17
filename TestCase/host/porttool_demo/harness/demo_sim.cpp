/*
 * The step-6 demo's protocol core on a PC, talking over stdin/stdout. Case T4-05.
 *
 * fixture_proto.cpp is compiled unchanged from the board package; only the
 * hooks of fixture_hw.h are replaced here. So everything the panel sees -
 * replies, refusals, caps, frames, the echo counter - is the demo's own code,
 * while every reading below is invented.
 *
 * Peers are played the way the fixture firmware's sim board plays them
 * (src/simboard/c/sim_main.c): RS485, CAN and USB echo each number back.
 * Ethernet is a real TCP server on 127.0.0.1:<port>, so the Go test drives
 * mode=mirror over a socket on both boards alike.
 *
 * Build: go test ./TestCase/host/porttool_demo/ -args -build (build_test.go).
 */

#include "fixture_hw.h"
#include "fixture_proto.h"

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <deque>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
#include <io.h>
#include <fcntl.h>
typedef SOCKET sock_t;
#define SOCK_NONE INVALID_SOCKET
#define sock_close closesocket
static bool would_block() { return WSAGetLastError() == WSAEWOULDBLOCK; }
static void nonblock(sock_t s) { u_long on = 1; ioctlsocket(s, FIONBIO, &on); }
#else
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>
typedef int sock_t;
#define SOCK_NONE (-1)
#define sock_close close
static bool would_block() { return errno == EAGAIN || errno == EWOULDBLOCK; }
static void nonblock(sock_t s) { fcntl(s, F_SETFL, fcntl(s, F_GETFL, 0) | O_NONBLOCK); }
#endif

static const auto t0 = std::chrono::steady_clock::now();

/* ---- control port and clock --------------------------------------------- */

void fx_out(const char *text) { fputs(text, stdout); }

uint32_t fx_millis(void)
{
    auto ms = std::chrono::duration_cast<std::chrono::milliseconds>(
        std::chrono::steady_clock::now() - t0).count();
    return (uint32_t)ms + 1000U;
}

void fx_uid(uint32_t words[3]) { words[0] = words[1] = words[2] = 0x00FACADEU; }
void fx_led(int) {}
void fx_line_baud(uint32_t) {}   /* a pipe has no rate */

/* A blank calibration area, as erased flash reads. */
size_t fx_cal_area(uint8_t *out, size_t len)
{
    size_t n = len < 56U ? len : 56U;
    memset(out, 0xFF, n);
    return n;
}

/* ---- digital and analog: fixed readings ---------------------------------- */

void fx_din_init(void) {}
uint8_t fx_din_read(void) { return 0xFFU; }
void fx_dout_init(void) {}
void fx_dout_set(int, int) {}
void fx_relay_init(void) {}
void fx_relay_set(int, int) {}

int fx_analog_init(void) { return 1; }
uint32_t fx_vdda_mv(void) { return 2501U; }

int fx_ain_read(int ch, uint32_t *raw, uint32_t *mv)
{
    *raw = ch == 1 ? 54300U : 32500U;
    *mv = *raw * 2500U / 65535U;
    return 1;
}

int32_t fx_ai1_mv(void) { return 8302; }
int32_t fx_ai2_ua(void) { return 10000; }
const char *fx_cal_status(void) { return "ok"; }

uint32_t fx_aout_set_mv(int, uint32_t mv)
{
    return mv * 4095U / 2500U;
}

int fx_aout_fault(int) { return 0; }

int fx_temp_read(int ch, uint32_t *mv)
{
    *mv = ch == 1 ? 738U : 751U;
    return 1;
}

/* ---- RS485, CAN: the played peer answers each number at once -------------- */

static std::deque<uint8_t> rs485_in;

int fx_rs485_begin(uint32_t) { rs485_in.clear(); return 1; }
void fx_rs485_end(void) {}

void fx_rs485_send(const char *data, size_t len)
{
    rs485_in.insert(rs485_in.end(), data, data + len);
}

int fx_rs485_read(uint8_t *b)
{
    if (rs485_in.empty()) { return 0; }
    *b = rs485_in.front();
    rs485_in.pop_front();
    return 1;
}

struct can_frame_t { uint32_t id; uint8_t len; uint8_t d[8]; };
static std::deque<can_frame_t> can_in;
static bool can_open;

int fx_can_begin(uint32_t) { can_in.clear(); can_open = true; return 1; }
void fx_can_end(void) { can_open = false; }
int fx_can_alive(void) { return can_open; }

int fx_can_write(uint32_t id, const uint8_t *data, uint8_t len)
{
    can_frame_t f = { id, len, { 0 } };
    memcpy(f.d, data, len);
    can_in.push_back(f);
    return 1;
}

int fx_can_read(uint32_t *id, uint8_t *data, uint8_t *len)
{
    if (can_in.empty()) { return 0; }
    *id = can_in.front().id;
    *len = can_in.front().len;
    memcpy(data, can_in.front().d, *len);
    can_in.pop_front();
    return 1;
}

void fx_can_counters(uint32_t *tec, uint32_t *rec, uint32_t *lec) { *tec = *rec = *lec = 0; }

/* ---- Ethernet: a real TCP server on 127.0.0.1 ----------------------------- */

static sock_t eth_listener = SOCK_NONE, eth_client = SOCK_NONE;
static std::deque<uint8_t> eth_in;

static void eth_drop(void)
{
    if (eth_client != SOCK_NONE) { sock_close(eth_client); eth_client = SOCK_NONE; }
    eth_in.clear();
}

static void eth_service(void)
{
    uint8_t buf[4096];

    if (eth_listener == SOCK_NONE) { return; }
    if (eth_client == SOCK_NONE) {
        sock_t c = accept(eth_listener, NULL, NULL);
        if (c == SOCK_NONE) { return; }
        nonblock(c);
        eth_client = c;
    }
    while (eth_in.size() < 65536U) {
        int n = recv(eth_client, (char *)buf, sizeof(buf), 0);
        if (n > 0) { eth_in.insert(eth_in.end(), buf, buf + n); continue; }
        if (n < 0 && would_block()) { break; }
        eth_drop();
        break;
    }
}

int fx_eth_listen(uint16_t port, char *err, size_t err_len)
{
    struct sockaddr_in a;

    fx_eth_close();
    eth_listener = socket(AF_INET, SOCK_STREAM, 0);
    memset(&a, 0, sizeof(a));
    a.sin_family = AF_INET;
    a.sin_port = htons(port);
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (eth_listener == SOCK_NONE || bind(eth_listener, (struct sockaddr *)&a, sizeof(a)) != 0 ||
        listen(eth_listener, 1) != 0) {
        if (eth_listener != SOCK_NONE) { sock_close(eth_listener); eth_listener = SOCK_NONE; }
        snprintf(err, err_len, "port %u is already bound", (unsigned)port);
        return 0;
    }
    nonblock(eth_listener);
    return 1;
}

void fx_eth_close(void)
{
    eth_drop();
    if (eth_listener != SOCK_NONE) { sock_close(eth_listener); eth_listener = SOCK_NONE; }
}

void fx_eth_address(const uint8_t *) {}

/* A LAN8742A at MDIO address 0 with a 100 Mbit/s full-duplex link. */
int fx_eth_phy_read(uint32_t addr, uint32_t reg, uint16_t *val)
{
    static const uint16_t regs[32] = { 0x1000U, 0x782DU, 0x0007U, 0xC131U };
    *val = addr != 0U ? 0xFFFFU : reg == 31U ? 0x1058U : reg < 4U ? regs[reg] : 0U;
    return 1;
}

int fx_eth_link(void) { return 1; }
int fx_eth_connected(void) { eth_service(); return eth_client != SOCK_NONE; }
void fx_eth_ip(char *out, size_t len) { snprintf(out, len, "127.0.0.1"); }
size_t fx_eth_avail(void) { eth_service(); return eth_in.size(); }
size_t fx_eth_room(void) { return eth_client != SOCK_NONE ? 8192U : 0U; }

size_t fx_eth_read(uint8_t *buf, size_t len)
{
    size_t n = len < eth_in.size() ? len : eth_in.size();
    for (size_t i = 0; i < n; i++) { buf[i] = eth_in.front(); eth_in.pop_front(); }
    return n;
}

size_t fx_eth_write(const uint8_t *buf, size_t len)
{
    size_t done = 0;

    while (done < len && eth_client != SOCK_NONE) {
        int n = send(eth_client, (const char *)buf + done, (int)(len - done), 0);
        if (n > 0) { done += (size_t)n; }
        else if (n < 0 && would_block()) { std::this_thread::sleep_for(std::chrono::milliseconds(1)); }
        else { eth_drop(); }
    }
    return done;
}

/* ---- USB: the played peer answers each number at once --------------------- */

static std::deque<uint8_t> usb_in;

void fx_usb_begin(void) { usb_in.clear(); }
const char *fx_usb_state(void) { return "cfg"; }
size_t fx_usb_avail(void) { return usb_in.size(); }
size_t fx_usb_room(void) { return 1024U; }

size_t fx_usb_read(uint8_t *buf, size_t len)
{
    size_t n = len < usb_in.size() ? len : usb_in.size();
    for (size_t i = 0; i < n; i++) { buf[i] = usb_in.front(); usb_in.pop_front(); }
    return n;
}

size_t fx_usb_write(const uint8_t *buf, size_t len)
{
    usb_in.insert(usb_in.end(), buf, buf + len);
    return len;
}

int fx_sd_detected(void) { return 1; }

/* A 32 GB FAT32 card; files live in memory. */
int fx_sd_identify(fx_sd_card_t *c)
{
    c->blocks = 62333952U;
    c->block_size = 512U;
    c->card_class = 1461U;
    c->v2x = 1;
    return 1;
}

int fx_sd_read_block(uint32_t, uint8_t *buf)
{
    memset(buf, 0, 512);
    memcpy(&buf[82], "FAT32   ", 8);
    buf[510] = 0x55U;
    buf[511] = 0xAAU;
    return 1;
}

void fx_sd_release(void) {}

static std::vector<uint8_t> sd_file;
static size_t sd_pos;

int fx_sd_mount(void) { return 1; }

int fx_sd_open(const char *, int write)
{
    if (write) { sd_file.clear(); }
    sd_pos = 0;
    return 0;
}

int fx_sd_write(const uint8_t *buf, uint32_t n, uint32_t *done)
{
    sd_file.insert(sd_file.end(), buf, buf + n);
    *done = n;
    return 0;
}

int fx_sd_read(uint8_t *buf, uint32_t n, uint32_t *done)
{
    size_t k = sd_file.size() - sd_pos < n ? sd_file.size() - sd_pos : n;
    memcpy(buf, sd_file.data() + sd_pos, k);
    sd_pos += k;
    *done = (uint32_t)k;
    return 0;
}

int fx_sd_sync(void) { return 0; }
int fx_sd_close(void) { return 0; }

/* 64 MiB of host memory standing in for the SDRAM. */
volatile uint32_t *fx_sdram(void)
{
    static uint32_t *mem = (uint32_t *)malloc(0x04000000U);
    return mem;
}

/* ---- the command channel -------------------------------------------------- */

static std::mutex q_lock;
static std::string q;
static volatile bool input_closed;

static void reader(void)
{
    char line[512];

    while (fgets(line, sizeof(line), stdin) != NULL) {
        std::lock_guard<std::mutex> g(q_lock);
        q += line;
    }
    input_closed = true;
}

int main(void)
{
#ifdef _WIN32
    WSADATA wsa;
    (void)WSAStartup(MAKEWORD(2, 2), &wsa);
    _setmode(_fileno(stdout), _O_BINARY);
#endif
    setvbuf(stdout, NULL, _IOFBF, 1 << 16);

    printf("\r\n=== SIMULATED step-6 demo - NOT A BOARD ===\r\n");
    fx_begin();
    fflush(stdout);

    std::thread(reader).detach();
    for (;;) {
        std::string in;
        {
            std::lock_guard<std::mutex> g(q_lock);
            in.swap(q);
        }
        for (char c : in) { fx_feed(c); }
        fx_poll(fx_millis());
        fflush(stdout);
        if (input_closed && in.empty()) { break; }
        std::this_thread::sleep_for(std::chrono::milliseconds(2));
    }
    fx_eth_close();
    return 0;
}
