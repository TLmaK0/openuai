//go:build darwin

#include <dispatch/dispatch.h>

extern void trayStartOnMain(void);

static void callStart(void *ctx) {
  trayStartOnMain();
}

void trayDispatchStart(void) {
  dispatch_async_f(dispatch_get_main_queue(), NULL, callStart);
}
