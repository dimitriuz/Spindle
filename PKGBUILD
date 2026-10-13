# Maintainer: Dmitrii Leshchenko <dmitrii@example.com>
pkgname=cliamp-web
pkgver=1.0.0
pkgrel=1
pkgdesc="LAN web remote bridge for cliamp"
arch=(x86_64 aarch64)
license=(MIT)
makedepends=(go)
source=("git+https://github.com/<user>/cliamp-plugin-web#tag=v$pkgver")
sha256sums=("SKIP")
build() {
  cd cliamp-plugin-web
  make build
}
package() {
  install -Dm755 cliamp-plugin-web/dist/cliamp-web "$pkgdir/usr/bin/cliamp-web"
  install -Dm644 cliamp-plugin-web/LICENSE "$pkgdir/usr/share/licenses/$pkgname/LICENSE"
}
