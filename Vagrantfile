# -*- mode: ruby -*-
# vi: set ft=ruby :

# Fail if Vagrant version is too old
begin
  Vagrant.require_version ">= 1.9.0"
rescue NoMethodError
  $stderr.puts "This Vagrantfile requires Vagrant version >= 1.9.0"
  exit 1
end

# Vagrantfile API/syntax version. Don't touch unless you know what you're doing!
VAGRANTFILE_API_VERSION = "2"

Vagrant.configure(VAGRANTFILE_API_VERSION) do |config|
  config.vm.box = "ubuntu/focal64"

  # Sync all project directories to the VM (owned by root)
  config.vm.synced_folder ".", "/vagrant", create: true, group: "root", owner: "root"
  config.vm.synced_folder "./flynn", "/root/go/src/github.com/flynn/flynn", create: true, group: "root", owner: "root"
  config.vm.synced_folder "./flynn-discovery", "/root/go/src/github.com/flynn/flynn-discovery", create: true, group: "root", owner: "root"
  config.vm.synced_folder "./go-tuf", "/root/go/src/github.com/flynn/go-tuf", create: true, group: "root", owner: "root"
  config.vm.synced_folder "./repository-service-tuf", "/root/repository-service-tuf", create: true, group: "root", owner: "root"

  if Vagrant.has_plugin?("vagrant-vbguest")
    # vagrant-vbguest can cause the VM to not start: https://github.com/flynn/flynn/issues/2874
    config.vbguest.auto_update = false
  end

  # Override locale settings. Avoids host locale settings being sent via SSH
  ENV['LC_ALL']="en_US.UTF-8"
  ENV['LANG']="en_US.UTF-8"
  ENV['LANGUAGE']="en_US.UTF-8"

  # Network configuration for services
  # Flynn Discovery
  config.vm.network "forwarded_port", guest: 1111, host: 1111, host_ip: "127.0.0.1"
  # Repository Service TUF API
  config.vm.network "forwarded_port", guest: 80, host: 8000, host_ip: "127.0.0.1"
  # Repository Service TUF Web Server
  config.vm.network "forwarded_port", guest: 8080, host: 8080, host_ip: "127.0.0.1"
  # PostgreSQL
  config.vm.network "forwarded_port", guest: 5432, host: 15432, host_ip: "127.0.0.1"
  config.vm.network "forwarded_port", guest: 5433, host: 15433, host_ip: "127.0.0.1"
  # Redis
  config.vm.network "forwarded_port", guest: 6379, host: 16379, host_ip: "127.0.0.1"
  # MongoDB
  config.vm.network "forwarded_port", guest: 27017, host: 27017, host_ip: "127.0.0.1"

  # VAGRANT_MEMORY          - instance memory, in MB
  # VAGRANT_CPUS            - instance virtual CPUs
  config.vm.provider "virtualbox" do |v, override|
    v.memory = ENV["VAGRANT_MEMORY"] || 8192  # Increased for running multiple services
    v.cpus = ENV["VAGRANT_CPUS"] || 4

    # Enable nested virtualization if needed for containers
    v.customize ["modifyvm", :id, "--nested-hw-virt", "on"]
  end

  # Provision with Ansible (builds everything and installs to /usr/local/flynn/bin)
  #config.vm.provision "ansible" do |ansible|
  #  ansible.playbook = "playbook.yml"
  #  ansible.config_file = "ansible.cfg"
  #end

  # Display helpful information after provisioning
  config.vm.provision "shell", privileged: true, inline: <<-SHELL
      if true; then
        sudo su -l

        apt-get update
        add-apt-repository ppa:longsleep/golang-backports -y
        apt-get install ca-certificates curl make golang
        install -m 0755 -d /etc/apt/keyrings
        curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
        chmod a+r /etc/apt/keyrings/docker.asc

        # Add the repository to Apt sources:
        echo \
          "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \
          $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}") stable" | \
          tee /etc/apt/sources.list.d/docker.list > /dev/null
        apt-get update
        apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin jq net-tools ifupdown zfsutils-linux

        cd /root/go/src/github.com/flynn/go-tuf/
        docker compose up --build -d

        # Whenever the keys expire, you have to run this
        # script again, and then clean and build flynn
        ./update_keys_in_flynn.sh

        cd /root/go/src/github.com/flynn/flynn-discovery
        docker compose up --build -d

        cd /root/go/src/github.com/flynn/flynn
        make
        mkdir -p /etc/flynn
        mkdir -p /tmp/discoverd-data

        make clean
        make

        #./script/clean-flynn
        #./script/build-flynn
        #./script/flynn-builder build --version=dev --verbose
        #./build/bin/flynn-builder build

        CGO_ENABLED=0 go build -o build/bin/flannel-wrapper ./flannel/wrapper

        export CLUSTER_DOMAIN=flynn.local
        export DISCOVERD=192.0.2.200:1111
        export FLYNN_DISCOVERY_SERVER=http://localhost:8180
        export EXTERNAL_IP=192.0.2.200
        export LISTEN_IP=192.0.2.200
        export PORT_0=1111
        export DISCOVERD_PEERS=192.0.2.200:1111
        export PATH="/root/go/src/github.com/flynn/flynn/build/bin:$PATH"
        export DISCOVERY_URL=`DISCOVERY_SERVER=http://localhost:8180 ./build/bin/flynn-host init --init-discovery`

        ./script/start-flynn-host 0
        sleep 5
        ./script/start-discoverd.sh
        sleep 5
        ./script/start-flanneld.sh
      fi



      exit
  SHELL
end
