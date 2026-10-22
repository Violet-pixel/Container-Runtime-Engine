#!/usr/bin/env bash
set -euo pipefail

out="${1:-rootfs.tar.gz}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

bins=(sh bash ls cat cp mv rm mkdir rmdir touch ln chmod echo printf sleep env id hostname uname ps grep sed awk head tail wc tr sort uniq date df du free mount ip ping kill dd yes true false)

copy_bin() {
  local path="$1"
  mkdir -p "$work$(dirname "$path")"
  cp -L "$path" "$work$path"
  ldd "$path" 2>/dev/null | grep -o '/[^ )]*' | while read -r lib; do
    [ -e "$lib" ] || continue
    mkdir -p "$work$(dirname "$lib")"
    cp -Ln "$lib" "$work$lib" 2>/dev/null || true
  done
}

for b in "${bins[@]}"; do
  p="$(command -v "$b" 2>/dev/null || true)"
  if [ -n "$p" ] && [ -f "$p" ]; then
    copy_bin "$p"
  fi
done

mkdir -p "$work"/{proc,sys,dev,tmp,root,etc,var/log,usr/local/bin}
chmod 1777 "$work/tmp"
printf 'root:x:0:0:root:/root:/bin/sh\nnobody:x:65534:65534:nobody:/:/bin/sh\n' > "$work/etc/passwd"
printf 'root:x:0:\nnogroup:x:65534:\n' > "$work/etc/group"
printf 'NAME="Silo rootfs"\nID=silo\n' > "$work/etc/os-release"
if [ ! -e "$work/bin/sh" ] && [ -e "$work/usr/bin/sh" ]; then
  mkdir -p "$work/bin"
  ln -s /usr/bin/sh "$work/bin/sh"
fi

tar -C "$work" -czf "$out" .
echo "rootfs criado em $out ($(du -h "$out" | cut -f1))"
echo "importe com: sudo silo import $out base:1 /bin/sh"
