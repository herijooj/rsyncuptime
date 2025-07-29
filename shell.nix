# Nix shell para desenvolvimento do servidor web rsyncuptime

{ pkgs ? import <nixpkgs> {} }:

pkgs.mkShell {
  buildInputs = [
    pkgs.go
    pkgs.rsync
    pkgs.jq
  ];

  shellHook = ''
  '';
}
