// Loader for needs_wasm.wasm: the same needs core as the command line,
// with JSON requests and responses (see needs_core::api).
//
//   const needs = await loadNeeds(bytesOrResponse);
//   const { report } = needs.handle({ op: "lint", config, sources, context });

export async function loadNeeds(source) {
  const { instance } = typeof Response !== "undefined" && source instanceof Response
    ? await WebAssembly.instantiateStreaming(source, {})
    : await WebAssembly.instantiate(source, {});
  const { memory, needs_alloc, needs_handle, needs_free } = instance.exports;
  const enc = new TextEncoder();
  const dec = new TextDecoder();
  return {
    handle(request) {
      return JSON.parse(this.handleRaw(typeof request === "string" ? request : JSON.stringify(request)));
    },
    handleRaw(requestText) {
      const bytes = enc.encode(requestText);
      const ptr = needs_alloc(bytes.length);
      new Uint8Array(memory.buffer, ptr, bytes.length).set(bytes);
      const packed = needs_handle(ptr, bytes.length);
      const rptr = Number(packed >> 32n), rlen = Number(packed & 0xffffffffn);
      const text = dec.decode(new Uint8Array(memory.buffer, rptr, rlen));
      needs_free(rptr, rlen);
      return text;
    },
  };
}
