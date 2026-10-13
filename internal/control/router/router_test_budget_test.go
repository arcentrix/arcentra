package router

// Shared runners execute race-instrumented database and password work alongside
// other packages. Fiber's one-second default is not an application latency SLA.
// Keep all router requests bounded without disabling timeouts or weakening checks.
const routerTestTimeoutMillis = 10_000
