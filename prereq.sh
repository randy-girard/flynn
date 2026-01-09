set -xeo pipefail
export DEBIAN_FRONTEND=noninteractive
source /etc/lsb-release

if [[ "$DISTRIB_RELEASE" == "14.04" ]]; then
  service cron stop
elif [[ "$DISTRIB_RELEASE" == "16.04" ]]; then
  systemctl stop cron
fi

echo "options single-request-reopen" >> /etc/resolvconf/resolv.conf.d/base
resolvconf -u

apt-get update
apt-get install -y nfs-common
apt-get purge -y puppet byobu juju ruby || true
apt-get install -y dkms

VBOX_VERSION=$(cat /root/go/src/github.com/flynn/flynn/.vbox_version)
VBOX_ISO="VBoxGuestAdditions_${VBOX_VERSION}.iso"

mount -o loop "/root/go/src/github.com/flynn/flynn/${VBOX_ISO}" /mnt
yes | sh /mnt/VBoxLinuxAdditions.run || true
umount /mnt
echo "flynn" > /etc/hostname
echo "127.0.1.1 flynn" >> /etc/hosts
hostname -F /etc/hostname

perl -p -i -e \
  's/GRUB_CMDLINE_LINUX=""/GRUB_CMDLINE_LINUX="cgroup_enable=memory swapaccount=1"/g' \
  /etc/default/grub
update-grub

groupadd fuse || true
usermod -a -G fuse vagrant

apt-key adv --keyserver keyserver.ubuntu.com \
  --recv 27947298A222DFA46E207200B34FBCAA90EA7F4E

echo "deb http://ppa.launchpad.net/titanous/tup/ubuntu trusty main" \
  > /etc/apt/sources.list.d/tup.list

apt-get update

apt-get install -y \
  curl \
  git \
  iptables \
  make \
  squashfs-tools \
  tup \
  vim-tiny \
  libsasl2-dev

chmod ug+s /usr/bin/tup
sed -i 's/#user_allow_other/user_allow_other/' /etc/fuse.conf

apt-get autoremove -y
apt-get clean

CUR_KERNEL=$(uname -r | sed 's/-*[a-z]//g' | sed 's/-386//g')

dpkg -l | \
  egrep 'linux-(image|headers|ubuntu-modules|restricted-modules)' | \
  egrep -v "${CUR_KERNEL}|generic|server|common|virtual|xen|ec2" | \
  awk '{print $2}' | \
  xargs apt-get purge -y || true

rm -f /var/lib/dhcp/* || true

sed -i 's/^Prompt=.*$/Prompt=never/' /etc/update-manager/release-upgrades

apt-get install -y --install-recommends \
  linux-generic-hwe-16.04 \
  -o Dpkg::Options::="--force-confdef" \
  -o Dpkg::Options::="--force-confold"

apt-get autoremove -y

apt-get dist-upgrade -y \
  -o Dpkg::Options::="--force-confdef" \
  -o Dpkg::Options::="--force-confold"
