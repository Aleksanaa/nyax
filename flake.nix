{
  description = "Nyax — Nix language parser and Nix-compatible file hasher in Go";

  inputs = {
    flake-parts.url = "github:hercules-ci/flake-parts";
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs = inputs: inputs.flake-parts.lib.mkFlake { inherit inputs; } {
    systems = [ "x86_64-linux" "aarch64-linux" ];
    perSystem = { pkgs, ... }: {
      packages = rec {
        nyax = with pkgs; buildGoModule {
          name = "nyax";

          src = lib.cleanSource ./.;

          nativeBuildInputs = [
            gotools
            ragel
          ];

          preBuild = ''
            go generate ./...
          '';

          vendorHash = "sha256-sjxM57fA8EYKw09gCr6uGV+0n/3ii0A/gtRN5u3Tewc=";

          proxyVendor = true;

          ldflags = [ "-s" "-w" ];

          meta.license = lib.licenses.lgpl21Plus;
        };
        default = nyax;
      };
      devShells.default = with pkgs; mkShell {
        packages = [
          go
          gotools
          ragel
          gore # test go feature
          graphviz # go tool pprof
        ];
      };
    };
  };
}
