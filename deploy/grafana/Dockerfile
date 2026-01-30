FROM grafana/grafana:10.2.2

# Copy provisioning files
COPY provisioning/datasources/prometheus.yml /etc/grafana/provisioning/datasources/prometheus.yml
COPY dashboards/*.json /var/lib/grafana/dashboards/

# Copy dashboard provisioning config
COPY provisioning/dashboards.yml /etc/grafana/provisioning/dashboards/dashboards.yml

# Environment variables
ENV GF_SECURITY_ADMIN_PASSWORD=admin
ENV GF_USERS_ALLOW_SIGN_UP=false

EXPOSE 3000
