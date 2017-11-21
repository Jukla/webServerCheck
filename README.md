# webServerCheck

This program helps you checking the functionality of your webserver. 

You can parse a configuration file with several vhosts in it for domains to check. The regex of re_domain is for parsing nginx.conf files. For other types like apache edit this regex.

Two URI can be test against every vhost: uri1 and uri2

Regular status messages like statistics and domains that are ok, are logged in './main."timestamp".log'
Problems/errors are logged in './error."timestamp".log'


ToDo:
* parameterize naming and location of log files
* parameterize number and value of uri's
* Implement a blacklist of domains that are not checked, even if they are found in webservers config file



If you see problems with this code or parts that can be done better, please comment or push a commit. Thank you.
