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
        # The generator binary.
        generator = pkgs.buildGoModule {
          pname = "blog-generator";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-bg9jqQUR5McQrjcnDYWDKn8yNR8B0JheCs+c1dd3a64=";
        };

        # The built website: `nix build` -> ./result is the site.
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
            echo "Serving $out on http://localhost:8000"
            exec ${pkgs.python3}/bin/python3 -m http.server -d "$out" 8000
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
