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
  config.vm.box = "ubuntu/bionic64"

  # Sync all project directories to the VM (owned by root)
  config.vm.synced_folder ".", "/root/go/src/github.com/flynn/flynn", create: true, group: "root", owner: "root"
  config.vm.synced_folder "./flynn-discovery", "/root/go/src/github.com/flynn/flynn-discovery", create: true, group: "root", owner: "root"
  config.vm.synced_folder "./go-tuf", "/root/go/src/github.com/flynn/go-tuf", create: true, group: "root", owner: "root"

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
      sudo su -l

      cd /root/go/src/github.com/flynn/flynn
      ./setup.sh
      ./build.sh

      #}export FLYNN_HOST_CHECKSUM=3190e053652b59c34982b6ac03d8a3fac0549fe2d975cf76b7bb42cf34e0985c623032f8a48215a951168562e9064d6c913983d613aa464332e620c45ddc6ce5
      #/usr/bin/install-flynn --repo http://localhost:8080 --version dev


      exit
  SHELL
end
