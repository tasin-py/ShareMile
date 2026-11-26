ShareMile for Linux
===================

Install (no root needed):

    ./install.sh

It installs the system libraries ShareMile needs if they're missing (WebKitGTK 4.1
and GTK 3, through your package manager: pacman, apt, dnf or zypper), puts ShareMile
in your app menu, and can add a desktop icon, pin it to the dock (GNOME), open the
project page and start it. Every option is on by default; answer n to skip one,
or use -y to accept all. sudo ./install.sh installs it for every user in /opt/sharemile.

Run without installing:  ./sharemile
Remove:                  ~/.local/share/sharemile/uninstall.sh

Works on Arch, Manjaro, EndeavourOS, Ubuntu 22.04+, Debian 12+, Linux Mint 21+,
Fedora 36+, openSUSE Tumbleweed and other distributions with glibc 2.34 or newer.
The tray icon appears on KDE, Cinnamon, XFCE, MATE, Budgie, LXQt, and on GNOME with
the AppIndicator extension (included in Ubuntu, Fedora asks you to add it).

On a server without a desktop, run  sharemile --headless  and open the address it
prints in a browser.

https://github.com/tasin-py/ShareMile
