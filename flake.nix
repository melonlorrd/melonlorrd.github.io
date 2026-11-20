{
  description = "Melonlorrd's Blog: a tiny Go static site generator";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        generator = pkgs.buildGoModule {
          pname = "blog-generator";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-bg9jqQUR5McQrjcnDYWDKn8yNR8B0JheCs+c1dd3a64=";
        };

        site = pkgs.runCommand "melonlorrd-site" { nativeBuildInputs = [ generator ]; } ''
          cp -r ${./.}/. src
          chmod -R u+w src
          cd src
          website
          cp -r build $out
        '';

        default = site;
      });

      apps = forAllSystems (pkgs: {
        # `nix run .#serve`: build the site and serve it on http://localhost:8000
        serve = {
          type = "app";
          program = toString (pkgs.writeShellScript "serve" ''
            set -e
            out=$(${pkgs.nix}/bin/nix build --no-link --print-out-paths .#site)
            # Copy out of the Nix store: store files all have a 1970 mtime,
            # which makes browsers think nothing ever changed.
            dir=$(mktemp -d)
            trap 'rm -rf "$dir"' EXIT
            cp -rL "$out"/. "$dir"
            echo "Serving on http://localhost:8000 (Ctrl-C to stop)"
            ${pkgs.python3}/bin/python3 -m http.server -d "$dir" 8000
          '');
        };
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls pkgs.python3 ];
        };
      });
    };
}
