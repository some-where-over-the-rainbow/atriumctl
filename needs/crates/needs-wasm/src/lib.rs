//! The needs core for the browser: a plain WebAssembly ABI with JSON in and
//! out, no bindings generator. The console's `needs.js` wraps it.
//!
//! - `needs_alloc(len) -> ptr`: allocate `len` bytes for a request
//! - `needs_handle(ptr, len) -> u64`: handle the JSON request at `ptr`
//!   (and free it); returns `(resp_ptr << 32) | resp_len`
//! - `needs_free(ptr, len)`: free a response
//!
//! Requests and responses are those of `needs_core::api::handle`.

#[unsafe(no_mangle)]
pub extern "C" fn needs_alloc(len: usize) -> *mut u8 {
    let mut buf = Vec::<u8>::with_capacity(len);
    let ptr = buf.as_mut_ptr();
    std::mem::forget(buf);
    ptr
}

/// # Safety
/// `ptr` and `len` must come from `needs_alloc` or `needs_handle`.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn needs_free(ptr: *mut u8, len: usize) {
    if !ptr.is_null() {
        drop(unsafe { Vec::from_raw_parts(ptr, 0, len) });
    }
}

/// # Safety
/// `ptr` must come from `needs_alloc(len)` and hold `len` bytes of UTF-8.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn needs_handle(ptr: *mut u8, len: usize) -> u64 {
    let req = unsafe { Vec::from_raw_parts(ptr, len, len) };
    let resp = match std::str::from_utf8(&req) {
        Ok(s) => needs_core::api::handle(s).to_string(),
        Err(e) => format!("{{\"error\":\"request is not UTF-8: {e}\"}}"),
    };
    drop(req);
    let mut out = resp.into_bytes().into_boxed_slice();
    let (p, n) = (out.as_mut_ptr(), out.len());
    std::mem::forget(out);
    ((p as u64) << 32) | n as u64
}

/// Random bytes for getrandom's "custom" backend (see `.cargo/config.toml`).
/// Only hash-map seeds use them, so a fixed-key xorshift stream is enough;
/// the needs core's results never depend on them.
#[cfg(target_arch = "wasm32")]
#[unsafe(no_mangle)]
unsafe extern "Rust" fn __getrandom_v03_custom(
    dest: *mut u8,
    len: usize,
) -> Result<(), getrandom::Error> {
    use std::sync::atomic::{AtomicU64, Ordering};
    static STATE: AtomicU64 = AtomicU64::new(0x9E37_79B9_7F4A_7C15);
    let out = unsafe { std::slice::from_raw_parts_mut(dest, len) };
    for chunk in out.chunks_mut(8) {
        let mut x = STATE.fetch_add(0x9E37_79B9_7F4A_7C15, Ordering::Relaxed);
        x ^= x >> 30;
        x = x.wrapping_mul(0xBF58_476D_1CE4_E5B9);
        x ^= x >> 27;
        x = x.wrapping_mul(0x94D0_49BB_1331_11EB);
        x ^= x >> 31;
        chunk.copy_from_slice(&x.to_le_bytes()[..chunk.len()]);
    }
    Ok(())
}
